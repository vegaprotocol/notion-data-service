package notion

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	jnotionapi "github.com/jomei/notionapi"
	log "github.com/sirupsen/logrus"
	"github.com/vegaprotocol/notion-data-service/util"
)

// IgnoreDatabaseDuration define how often we can try to pull unknown databases
// when database pull fails
const IgnoreDatabaseDuration = 5 * time.Minute

type DataItem struct {
	ID          string         `json:"id"`
	Properties  []DataProperty `json:"properties"`
	LastUpdated time.Time      `json:"last_updated"`
}

type DataProperty struct {
	Name   string   `json:"name"`   // e.g. Status
	Values []string `json:"values"` // One or more values, e.g. In Progress
}

type Service struct {
	notionAccessToken string
	databaseMap       map[string][]DataItem // ID -> DataItem
	lastUpdated       time.Time
	pollDuration      time.Duration
	timer             *time.Ticker

	mu                   sync.RWMutex
	ignoreDatabasesMutex sync.RWMutex
	wipMutex             sync.Mutex

	ignoredDatabases map[string]time.Time
	knownDatabases   []string
}

func NewDataService(notionAccessToken string, pollDuration time.Duration, knownDatabases []string) *Service {
	svc := &Service{
		notionAccessToken: notionAccessToken,
		pollDuration:      pollDuration,
		ignoredDatabases:  map[string]time.Time{},
		knownDatabases:    knownDatabases,
	}
	return svc
}

func (s *Service) Start() {
	log.Info("Notion data service started")

	s.mu.Lock()
	s.databaseMap = map[string][]DataItem{}
	s.lastUpdated = time.Date(2000, 1, 1, 1, 0, 0, 0, time.UTC)
	s.mu.Unlock()

	s.update()
	s.timer = util.Schedule(s.update, s.pollDuration)
}

func (s *Service) update() {
	log.Info("Begin update of Notion.so data")

	dbs := s.ListDatabases()
	if len(dbs) == 0 {
		log.Info("Service currently does not manage any notion database. Database is added when queried for a first time")
		return
	}
	res := map[string][]DataItem{}
	for _, databaseID := range dbs {
		log.Infof("Querying Notion.so for database %s", databaseID)
		dataItems, err := s.QueryDatabase(databaseID, false)
		if err != nil {
			log.WithError(err).Errorf("Failed to query notion database %s during update", databaseID)
		} else {
			res[databaseID] = dataItems
		}
	}

	s.mu.Lock()
	s.databaseMap = res
	s.mu.Unlock()

	log.Info("Completed update of Notion.so data")
}

func (s *Service) Stop() {
	if s.timer != nil {
		s.timer.Stop()
	}
	log.Info("Notion data service stopped")
}

// The Notion has deprecated the List databases endpoint. So there is no option to list them.
// Instead of List, we construct list of the databases map in the Service struct.
// Object is added to the database map when requested for a first time.
func (s *Service) ListDatabases() []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	result := []string{}

	for databaseID, _ := range s.databaseMap {
		result = append(result, databaseID)
	}

	return result
}

func (s *Service) QueryDatabaseCached(notionID string) ([]DataItem, error) {
	s.mu.RLock()
	if val, ok := s.databaseMap[notionID]; ok {
		log.Infof("The database %s fetched from cache", notionID)
		s.mu.RUnlock()
		return val, nil
	}
	s.mu.RUnlock()

	// This lock must be very rare. Usually We hit it only when there is an error for Notion
	s.wipMutex.Lock()
	defer s.wipMutex.Unlock()

	if s.IsDatabaseIgnored(notionID) {
		log.Warnf("The database %s cannot be fetched: database is ignored. Try agin later", notionID)
		return nil, fmt.Errorf("the %s database ignored", notionID)
	}

	// Check if another thread already fetched database
	if val, ok := s.databaseMap[notionID]; ok {
		return val, nil
	}

	log.Warnf("Cannot find database with ID %s in cache, trying to query it", notionID)
	res, err := s.QueryDatabase(notionID, true)
	if err != nil {
		s.ignoreNotionDatabase(notionID)
		return nil, fmt.Errorf("failed to query database %s: %w", notionID, err)
	}

	return res, nil
}

func (s *Service) QueryDatabase(notionID string, updateMapIfSuccess bool) ([]DataItem, error) {
	dbID := jnotionapi.DatabaseID(notionID)
	token := jnotionapi.Token(s.notionAccessToken)
	client := jnotionapi.NewClient(token)

	result := []DataItem{}
	var nextCursor jnotionapi.Cursor

	for {
		queryReq := jnotionapi.DatabaseQueryRequest{
			StartCursor: nextCursor,
			PageSize:    100,
		}
		page, err := client.Database.Query(context.Background(), dbID, &queryReq)

		if err != nil {
			log.WithError(err).Errorf("Failed to query notion database %s via API for cursor: %s", notionID, nextCursor)
			return nil, err
		}
		if page == nil {
			log.WithError(err).Errorf("Failed to find page for notion database %s via API for cursor: %s", notionID, nextCursor)
			return nil, err
		}
		res := s.processPageProperties(page.Results)

		result = append(result, res...)
		nextCursor = page.NextCursor

		if nextCursor == "" {
			break
		}
	}

	if updateMapIfSuccess && len(result) > 0 {
		s.mu.Lock()
		s.databaseMap[notionID] = result
		s.mu.Unlock()
	}

	log.Infof("Found and processed %d data items for %s database", len(result), notionID)

	return result, nil
}

func (s *Service) processPageProperties(pages []jnotionapi.Page) []DataItem {
	res := make([]DataItem, 0)
	for _, p := range pages {
		id := strings.ReplaceAll(p.ID.String(), "-", "") // make the item ID direct url friendly
		di := DataItem{ID: id, LastUpdated: p.LastEditedTime}
		di.Properties = make([]DataProperty, 0)
		// for each page result, look at properties
		for k, p := range p.Properties {
			pr := DataProperty{Name: k}

			pr.Values = make([]string, 0)
			t, ok := p.(*jnotionapi.TitleProperty)
			if ok {
				titleValues := []string{}

				for _, tt := range t.Title {
					titleValues = append(titleValues, tt.PlainText)
				}

				pr.Values = append(pr.Values, strings.Join(titleValues, ""))
			}
			rt, ok := p.(*jnotionapi.RichTextProperty)
			if ok {
				for _, tt := range rt.RichText {
					pr.Values = append(pr.Values, tt.PlainText)
				}
			}
			tp, ok := p.(*jnotionapi.TextProperty)
			if ok {
				for _, tt := range tp.Text {
					pr.Values = append(pr.Values, tt.PlainText)
				}
			}
			dp, ok := p.(*jnotionapi.DateProperty)
			if ok {
				if dp.Date.Start != nil {
					pr.Values = append(pr.Values, dp.Date.Start.String())
				}
				if dp.Date.End != nil {
					pr.Values = append(pr.Values, dp.Date.End.String())
				}
			}
			sp, ok := p.(*jnotionapi.SelectProperty)
			if ok {
				pr.Values = append(pr.Values, sp.Select.Name)
			}
			msp, ok := p.(*jnotionapi.MultiSelectProperty)
			if ok {
				for _, mso := range msp.MultiSelect {
					pr.Values = append(pr.Values, mso.Name)
				}
			}
			up, ok := p.(*jnotionapi.URLProperty)
			if ok {
				pr.Values = append(pr.Values, up.URL)
			}
			cb, ok := p.(*jnotionapi.CheckboxProperty)
			if ok {
				cbv := "false"
				if cb.Checkbox {
					cbv = "true"
				}
				pr.Values = append(pr.Values, cbv)
			}
			e, ok := p.(*jnotionapi.EmailProperty)
			if ok {
				pr.Values = append(pr.Values, e.Email)
			}
			pn, ok := p.(*jnotionapi.PhoneNumberProperty)
			if ok {
				pr.Values = append(pr.Values, pn.PhoneNumber)
			}
			fp, ok := p.(*jnotionapi.FormulaProperty)
			if ok {
				pr.Values = append(pr.Values, fp.Formula.String)
			}
			np, ok := p.(*jnotionapi.NumberProperty)
			if ok {
				pr.Values = append(pr.Values, fmt.Sprintf("%.5f", np.Number))
			}
			ct, ok := p.(*jnotionapi.CreatedTimeProperty)
			if ok {
				pr.Values = append(pr.Values, strconv.FormatInt(ct.CreatedTime.Unix(), 10))
			}
			et, ok := p.(*jnotionapi.LastEditedTimeProperty)
			if ok {
				pr.Values = append(pr.Values, strconv.FormatInt(et.LastEditedTime.Unix(), 10))
			}
			cr, ok := p.(*jnotionapi.CreatedByProperty)
			if ok {
				pr.Values = append(pr.Values, cr.CreatedBy.Name)
			}
			er, ok := p.(*jnotionapi.LastEditedByProperty)
			if ok {
				pr.Values = append(pr.Values, er.LastEditedBy.Name)
			}
			pp, ok := p.(*jnotionapi.PeopleProperty)
			if ok {
				for _, person := range pp.People {
					pr.Values = append(pr.Values, person.Name)
				}
			}
			di.Properties = append(di.Properties, pr)
		}
		res = append(res, di)
	}

	//for _, v := range res {
	//	fmt.Println("Prop: ", v.ID)
	//	for _, p := range v.Properties {
	//		fmt.Println(p.Name, " => ")
	//		for _, va := range p.Values {
	//			fmt.Print( va, " ")
	//		}
	//		fmt.Println()
	//	}
	//}

	return res
}

func (s *Service) IsDatabaseIgnored(notionID string) bool {
	s.ignoreDatabasesMutex.RLock()
	defer s.ignoreDatabasesMutex.RUnlock()

	if s.ignoredDatabases == nil {
		return false
	}

	normalizedID := normalizedNotionID(notionID)
	ignoredDbTime, ignoredDBExist := s.ignoredDatabases[normalizedID]
	if !ignoredDBExist {
		return false
	}

	return ignoredDbTime.Add(IgnoreDatabaseDuration).After(time.Now())
}

func (s *Service) ignoreNotionDatabase(notionID string) {
	s.ignoreDatabasesMutex.Lock()
	defer s.ignoreDatabasesMutex.Unlock()

	normalizedID := normalizedNotionID(notionID)
	// do not ignore known databases
	for _, knownID := range s.knownDatabases {
		if normalizedNotionID(knownID) == normalizedID {
			return
		}
	}

	log.Infof("The %s database added to ignored set", notionID)
	s.ignoredDatabases[normalizedID] = time.Now()
}

func normalizedNotionID(notionID string) string {
	notionID = strings.ReplaceAll(notionID, " ", "")
	return strings.ReplaceAll(notionID, "-", "")
}

func (s *Service) CleanupLoop() {
	ticker := time.NewTicker(2 * time.Minute)

	for {
		select {
		case <-ticker.C:
			s.ignoreDatabasesMutex.Lock()

			for notionID, ignoredDbTime := range s.ignoredDatabases {
				if ignoredDbTime.Add(IgnoreDatabaseDuration).After(time.Now()) {
					continue
				}

				log.Infof("Removing %s database from ignored databases", notionID)
				delete(s.ignoredDatabases, notionID)
			}

			s.ignoreDatabasesMutex.Unlock()
		}
	}
}
