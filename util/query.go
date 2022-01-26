package util

import (
	"net/http"
	"strconv"

	log "github.com/sirupsen/logrus"
)

func GetQuery(r *http.Request, key string) string {
	return r.URL.Query().Get(key)
}

func GetQueryInt(r *http.Request, key string) int64 {
	q := GetQuery(r, key)
	if len(q) > 0 {
		i, err := strconv.ParseInt(q, 10, 64)
		if err != nil {
			log.Warnf("Could not parse query string param %s %s to int", key, q)
			return -1
		}
		return i
	}
	return -1
}
