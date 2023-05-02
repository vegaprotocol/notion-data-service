package main

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/gorilla/handlers"
	"github.com/gorilla/mux"
	log "github.com/sirupsen/logrus"
	"github.com/vegaprotocol/notion-data-service/notion"
	"github.com/vegaprotocol/notion-data-service/util"
)

func main() {
	// Logger config
	log.SetFormatter(&log.JSONFormatter{})
	log.SetOutput(os.Stdout)
	log.SetLevel(log.InfoLevel)

	// Config
	conf, err := ReadConfig("config.yaml")
	if err != nil {
		log.Fatal("Failed to read config: ", err)
	}

	if len(conf.Port) <= 0 {
		log.Printf("Error: missing 'port' config (the address to bind the service to)")
		return
	}

	startService(conf)
}

func startService(conf ConfigVars) {
	log.Info("Starting up Notion.so data API service")

	pollDuration, err := time.ParseDuration(conf.NotionPollDuration)
	if err != nil {
		log.WithError(err).Warnf("Could not parse the NotionPollDuration %s", conf.NotionPollDuration)
		log.Warn("Using default duration of 5 minutes")
		pollDuration = 5 * time.Minute
	}

	log.Infof("Polling Notion.so every %s", pollDuration)
	log.Infof("API binding to %s:%s", conf.Host, conf.Port)

	notionService := notion.NewDataService(conf.NotionAccessToken, pollDuration, conf.KnownDatabases)

	router := mux.NewRouter()
	router.NotFoundHandler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		log.Infof("Route not found: %s - %s - %s - %s - %s - %s",
			r.Method, r.URL, r.Proto, r.RequestURI, r.RemoteAddr, string(r.ContentLength))
	})
	router.HandleFunc("/", RootHandler)
	router.HandleFunc("/status", StatusHandler)
	router.HandleFunc("/list", func(w http.ResponseWriter, r *http.Request) {
		ListHandler(w, r, notionService)
	})
	router.HandleFunc("/query", func(w http.ResponseWriter, r *http.Request) {
		QueryHandler(w, r, notionService)
	})

	srv := &http.Server{
		Addr:         conf.Host + ":" + conf.Port,
		WriteTimeout: time.Second * 15,
		ReadTimeout:  time.Second * 15,
		IdleTimeout:  time.Second * 60,
		Handler:      handlers.CORS(handlers.AllowedOrigins([]string{"*"}))(router),
	}

	// Start contributor service
	go func() {
		notionService.Start()
	}()

	// Start cleanup loop
	go func() {
		notionService.CleanupLoop()
	}()

	// Start api web service
	go func() {
		if err := srv.ListenAndServe(); err != nil && err.Error() != "http: Server closed" {
			log.WithError(err).Warn("Failed to serve")
		}
	}()

	c := make(chan os.Signal, 1)
	// We'll accept graceful shutdowns when quit via SIGINT (Ctrl+C)
	// SIGKILL, SIGQUIT or SIGTERM (Ctrl+/) will not be caught.
	signal.Notify(c, os.Interrupt)

	// Block until we receive our signal.
	<-c

	// Create a deadline to wait for (15 seconds).
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*15)
	defer cancel()

	// Signal to stop the contributor service
	notionService.Stop()

	// Doesn't block if no connections, but will otherwise wait
	// until the timeout deadline.
	srv.Shutdown(ctx)

	// Optionally, you could run srv.Shutdown in a goroutine and block on
	// <-ctx.Done() if your application should wait for other services
	// to finalize based on context cancellation.
	log.Info("Shutting down Notion.so data API service")
	os.Exit(0)
}

func QueryHandler(w http.ResponseWriter, r *http.Request, s *notion.Service) {
	w.Header().Set("Content-Type", "application/json")

	id := util.GetQuery(r, "id")
	noCache := util.GetQuery(r, "nocache")

	if len(id) < 10 {
		log.Errorf("Invalid ID passed to query data handler: %s", id)
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	dataItems, err := s.QueryDatabaseCached(id)
	if len(noCache) > 0 {
		dataItems, err = s.QueryDatabase(id, true)
	}

	response := DataResponse{
		LastUpdated: time.Now().Unix(),
		Items:       dataItems,
	}
	payload, err := json.Marshal(response)
	if err != nil {
		log.WithError(err).Error("Failed to marshal payload for databases")
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else {
		w.WriteHeader(http.StatusOK)
	}
	w.Write(payload)
}

func ListHandler(w http.ResponseWriter, r *http.Request, s *notion.Service) {
	w.Header().Set("Content-Type", "application/json")
	dbs := s.ListDatabases()
	response := ListResponse{
		LastUpdated: time.Now().Unix(),
		Databases:   dbs,
	}
	payload, err := json.Marshal(response)
	if err != nil {
		log.WithError(err).Error("Failed to marshal payload for databases")
		w.WriteHeader(http.StatusInternalServerError)
		return
	} else {
		w.WriteHeader(http.StatusOK)
	}
	w.Write(payload)
}

func StatusHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("{\"success\":true}"))
}

func RootHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html")
	w.WriteHeader(http.StatusOK)
	content := `<!doctype html>
<head>
<title>Notion Data Service</title>
</head>
<body>
<h1>Notion Data Service</h1>
<ul>
<li><a href="/status">Status</a></li>
<li><a href="/list">List data</a></li>
<li><a href="/query?id=">Query data</a></li>
</ul>
</body>
</html>`
	w.Write([]byte(content))
}

type ListResponse struct {
	LastUpdated int64    `json:"last_updated"`
	Databases   []string `json:"notion_databases"`
}

type DataResponse struct {
	LastUpdated int64             `json:"last_updated"`
	Items       []notion.DataItem `json:"notion_data"`
}
