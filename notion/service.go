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
	mu                sync.RWMutex
}

func NewDataService(notionAccessToken string, pollDuration time.Duration) *Service {
	svc := &Service{
		notionAccessToken: notionAccessToken,
		pollDuration:      pollDuration,
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

	dbs, err := s.ListDatabases()
	if err != nil {
		log.WithError(err).Error("Failed to load list of notion databases during update")
		return
	}
	res := map[string][]DataItem{}
	for k, v := range dbs {
		log.Infof("Querying Notion.so for database %s [%s]", k, v)
		dataItems, err := s.QueryDatabase(k, false)
		if err != nil {
			log.WithError(err).Errorf("Failed to query notion database %s during update", k)
		} else {
			log.Infof("Found and processed data items for %s [%s]", k, v)
			res[k] = dataItems
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

func (s *Service) ListDatabases() (map[string]string, error) {
	token := jnotionapi.Token(s.notionAccessToken)
	client := jnotionapi.NewClient(token)
	pagination := jnotionapi.Pagination{
		StartCursor: "",
		PageSize:    100,
	}
	response, err := client.Database.List(context.Background(), &pagination)
	if err != nil {
		return nil, err
	}
	results := map[string]string{}
	for _, r := range response.Results {
		title := ""
		for _, tt := range r.Title {
			title += tt.PlainText
		}
		results[r.ID.String()] = title
	}
	return results, nil
}

func (s *Service) QueryDatabaseCached(notionID string) ([]DataItem, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if val, ok := s.databaseMap[notionID]; ok {
		return val, nil
	}
	log.Warnf("Cannot find database with ID %s in cache", notionID)
	return nil, nil
}

func (s *Service) QueryDatabase(notionID string, updateMapIfSuccess bool) ([]DataItem, error) {
	dbID := jnotionapi.DatabaseID(notionID)
	token := jnotionapi.Token(s.notionAccessToken)
	client := jnotionapi.NewClient(token)
	queryReq := jnotionapi.DatabaseQueryRequest{
		StartCursor: "",
		PageSize:    100,
	}
	page, err := client.Database.Query(context.Background(), dbID, &queryReq)
	if err != nil {
		log.WithError(err).Errorf("Failed to query notion database %s via API", notionID)
		return nil, err
	}
	if page == nil {
		log.WithError(err).Errorf("Failed to find page for notion database %s via API", notionID)
		return nil, err
	}
	res := s.processPageProperties(page.Results)
	if updateMapIfSuccess && len(res) > 0 {
		s.mu.Lock()
		s.databaseMap[notionID] = res
		s.mu.Unlock()
	}
	return res, nil
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
				for _, tt := range t.Title {
					pr.Values = append(pr.Values, tt.PlainText)
				}
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
