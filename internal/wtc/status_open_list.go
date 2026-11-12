package wtc

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	statusOpenListLimit = 100
	statusGHOpenFields  = "number,title,state,isDraft,updatedAt,reviewDecision,mergeStateStatus,mergeable,baseRefName,headRefName,statusCheckRollup,reviewRequests,latestReviews"
)

type statusOpenPR struct {
	Number      string          `json:"number"`
	Branch      string          `json:"branch"`
	Fingerprint string          `json:"fingerprint"`
	Raw         json.RawMessage `json:"raw"`
	Started     time.Time       `json:"-"`
}

func statusOpenPRs(forge, slug string) (map[string]statusOpenPR, bool, error) {
	raw, err := statusSingleFlight("openprs", forge, slug, "open", func() ([]byte, error) {
		switch forge {
		case "github.com":
			return statusFetchGHOpenList(slug)
		case "bitbucket.org":
			return statusFetchBBOpenList(slug)
		default:
			return nil, fmt.Errorf("unsupported forge")
		}
	})
	if err != nil {
		return nil, false, err
	}
	list, truncated, started, err := statusParseOpenPRs(raw)
	if err != nil {
		return nil, false, err
	}
	byNumber := make(map[string]statusOpenPR, len(list))
	for _, pr := range list {
		if pr.Number != "" {
			pr.Started = started
			byNumber[pr.Number] = pr
		}
	}
	return byNumber, truncated, nil
}

func statusParseOpenPRs(raw []byte) ([]statusOpenPR, bool, time.Time, error) {
	var body struct {
		PRs       []statusOpenPR `json:"prs"`
		Truncated bool           `json:"truncated"`
		Started   int64          `json:"started"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, false, time.Time{}, err
	}
	return body.PRs, body.Truncated, time.Unix(0, body.Started), nil
}

func statusEncodeOpenPRs(prs []statusOpenPR, truncated bool, started time.Time) ([]byte, error) {
	return json.Marshal(struct {
		PRs       []statusOpenPR `json:"prs"`
		Truncated bool           `json:"truncated"`
		Started   int64          `json:"started"`
	}{PRs: prs, Truncated: truncated, Started: started.UnixNano()})
}

var statusBeforeOpenPublish func()

func statusDetailFromOpenItem(record PRRecord, slug, forge string, item statusOpenPR) (statusPRDetail, bool, bool) {
	// A changed list fingerprint cannot reopen a confirmed merge, even while
	// its merge time or checks are still being collected.
	if known, ok := statusRetainedMerge(record, slug, forge); ok {
		return known, true, true
	}
	if stamp, ok := statusReadForgeEntryAny("stamp", forge, slug, record.Number); ok && string(stamp) == item.Fingerprint {
		if cached, ok := statusReadForgeEntryAny("pr", forge, slug, record.Number); ok {
			if detail, err := statusParseForgeDetail(forge, cached, record); err == nil {
				return detail, true, true
			}
		}
	}
	detail, err := statusParseForgeDetail(forge, item.Raw, record)
	if err != nil {
		return statusPRDetail{}, false, false
	}
	if statusBeforeOpenPublish != nil {
		statusBeforeOpenPublish()
	}
	if !statusWriteForgeEntrySince("pr", forge, slug, record.Number, item.Raw, item.Started) {
		if cached, ok := statusReadObservedDetail(record, slug, forge, 365*24*time.Hour); ok {
			statusWriteForgeEntry("stamp", forge, slug, record.Number, []byte(item.Fingerprint))
			return cached, true, true
		}
	}
	statusWriteForgeEntry("stamp", forge, slug, record.Number, []byte(item.Fingerprint))
	return detail, true, false
}

type statusGHListItem struct {
	Number            int               `json:"number"`
	Title             string            `json:"title"`
	State             string            `json:"state"`
	IsDraft           bool              `json:"isDraft"`
	UpdatedAt         string            `json:"updatedAt"`
	ReviewDecision    string            `json:"reviewDecision"`
	MergeStateStatus  string            `json:"mergeStateStatus"`
	Mergeable         string            `json:"mergeable"`
	HeadRefName       string            `json:"headRefName"`
	ReviewRequests    []json.RawMessage `json:"reviewRequests"`
	LatestReviews     []json.RawMessage `json:"latestReviews"`
	StatusCheckRollup []struct {
		Conclusion string `json:"conclusion"`
		State      string `json:"state"`
		Status     string `json:"status"`
	} `json:"statusCheckRollup"`
}

func (p statusGHListItem) fingerprint() string {
	var checks strings.Builder
	for i, check := range p.StatusCheckRollup {
		if i > 0 {
			checks.WriteByte(',')
		}
		checks.WriteString(check.Status)
		checks.WriteByte(':')
		if check.Conclusion != "" {
			checks.WriteString(check.Conclusion)
		} else {
			checks.WriteString(check.State)
		}
	}
	return strings.Join([]string{
		p.UpdatedAt,
		strings.ToUpper(p.State),
		strconv.FormatBool(p.IsDraft),
		strings.ToUpper(p.ReviewDecision),
		p.MergeStateStatus,
		p.Mergeable,
		strconv.Itoa(len(p.ReviewRequests)),
		strconv.Itoa(len(p.LatestReviews)),
		checks.String(),
		p.Title,
	}, "|")
}

func statusFetchGHOpenList(slug string) ([]byte, error) {
	started := time.Now()
	raw, err := statusJSON("gh", "pr", "list", "--repo", slug, "--state", "open", "--limit", strconv.Itoa(statusOpenListLimit), "--json", statusGHOpenFields)
	if err != nil {
		return nil, err
	}
	var items []json.RawMessage
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("GitHub pull request list JSON: %w", err)
	}
	prs := make([]statusOpenPR, 0, len(items))
	for _, rawItem := range items {
		var item statusGHListItem
		if json.Unmarshal(rawItem, &item) != nil || item.Number == 0 {
			continue
		}
		prs = append(prs, statusOpenPR{
			Number:      fmt.Sprint(item.Number),
			Branch:      item.HeadRefName,
			Fingerprint: item.fingerprint(),
			Raw:         rawItem,
		})
	}
	return statusEncodeOpenPRs(prs, len(items) >= statusOpenListLimit, started)
}

type statusBBListItem struct {
	ID           int    `json:"id"`
	State        string `json:"state"`
	Draft        bool   `json:"draft"`
	Title        string `json:"title"`
	UpdatedOn    string `json:"updated_on"`
	CommentCount int    `json:"comment_count"`
	TaskCount    int    `json:"task_count"`
	Source       struct {
		Commit struct {
			Hash string `json:"hash"`
		} `json:"commit"`
		Branch struct {
			Name string `json:"name"`
		} `json:"branch"`
	} `json:"source"`
	Destination struct {
		Commit struct {
			Hash string `json:"hash"`
		} `json:"commit"`
		Branch struct {
			Name string `json:"name"`
		} `json:"branch"`
	} `json:"destination"`
	Participants []struct {
		Approved bool   `json:"approved"`
		State    string `json:"state"`
	} `json:"participants"`
}

func (p statusBBListItem) fingerprint() string {
	approved, changes := 0, 0
	for _, participant := range p.Participants {
		if participant.Approved {
			approved++
		}
		if strings.EqualFold(participant.State, "changes_requested") {
			changes++
		}
	}
	return strings.Join([]string{
		p.UpdatedOn,
		strconv.Itoa(p.CommentCount),
		strconv.Itoa(p.TaskCount),
		strings.ToUpper(p.State),
		strconv.FormatBool(p.Draft),
		strconv.Itoa(approved),
		strconv.Itoa(changes),
		p.Destination.Branch.Name,
		p.Source.Commit.Hash,
		p.Destination.Commit.Hash,
		p.Title,
	}, "|")
}

func statusFetchBBOpenList(slug string) ([]byte, error) {
	started := time.Now()
	user, pass := bitbucketCredentials()
	if user == "" || pass == "" {
		return nil, fmt.Errorf("no Bitbucket API credentials")
	}
	parts := strings.SplitN(slug, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid Bitbucket repository slug")
	}
	fields := "values.id,values.updated_on,values.comment_count,values.task_count,values.state,values.draft,values.title,values.source.branch.name,values.source.commit.hash,values.destination.branch.name,values.destination.commit.hash,values.participants.approved,values.participants.state,next"
	query := url.Values{}
	query.Set("state", "OPEN")
	query.Set("pagelen", "50")
	query.Set("fields", fields)
	endpoint := statusBBAPI + "/repositories/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/pullrequests?" + query.Encode()
	var items []statusBBListItem
	seen := map[string]bool{}
	truncated := false
	for page := 0; page < 10 && endpoint != ""; page++ {
		if page > 0 && (statusForgeCooling("bitbucket.org") || !statusTakeForgeToken("bitbucket.org")) {
			truncated = true
			break
		}
		raw, err := statusBBGET(endpoint, user, pass)
		if err != nil {
			if page == 0 {
				return nil, err
			}
			if statusRateLimited(err) {
				statusForgeCool("bitbucket.org", statusRetryAfter(err))
			}
			truncated = true
			break
		}
		var body struct {
			Values []statusBBListItem `json:"values"`
			Next   string             `json:"next"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, fmt.Errorf("Bitbucket pull request list JSON: %w", err)
		}
		items = append(items, body.Values...)
		more := body.Next != "" && len(body.Values) > 0 && !seen[body.Next] && statusSameOrigin(endpoint, body.Next)
		if !more {
			break
		}
		if page == 9 {
			truncated = true
			break
		}
		seen[body.Next] = true
		endpoint = body.Next
	}
	prs := make([]statusOpenPR, 0, len(items))
	for _, item := range items {
		if item.ID == 0 {
			continue
		}
		rawItem, err := json.Marshal(item)
		if err != nil {
			continue
		}
		prs = append(prs, statusOpenPR{
			Number:      fmt.Sprint(item.ID),
			Branch:      item.Source.Branch.Name,
			Fingerprint: item.fingerprint(),
			Raw:         rawItem,
		})
	}
	return statusEncodeOpenPRs(prs, truncated, started)
}

func statusFetchBBBranch(slug, branch string) ([]byte, error) {
	user, pass := bitbucketCredentials()
	if user == "" || pass == "" {
		return nil, fmt.Errorf("no Bitbucket API credentials")
	}
	parts := strings.SplitN(slug, "/", 2)
	if len(parts) != 2 {
		return nil, fmt.Errorf("invalid Bitbucket repository slug")
	}
	query := url.Values{}
	query.Set("state", "OPEN")
	query.Set("pagelen", "10")
	query.Set("fields", "values.id,values.source.branch.name")
	query.Set("q", `source.branch.name="`+statusBBQueryString(branch)+`"`)
	endpoint := statusBBAPI + "/repositories/" + url.PathEscape(parts[0]) + "/" + url.PathEscape(parts[1]) + "/pullrequests?" + query.Encode()
	return statusBBGET(endpoint, user, pass)
}

func statusBBQueryString(value string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(value)
}

func statusParseBBBranchList(raw []byte, branch string) (string, error) {
	var body struct {
		Values []struct {
			ID     int `json:"id"`
			Source struct {
				Branch struct {
					Name string `json:"name"`
				} `json:"branch"`
			} `json:"source"`
		} `json:"values"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return "", err
	}
	for _, pr := range body.Values {
		if pr.ID > 0 && pr.Source.Branch.Name == branch {
			return fmt.Sprint(pr.ID), nil
		}
	}
	return "", nil
}

func statusBBGET(endpoint, user, pass string) ([]byte, error) {
	request, err := http.NewRequest(http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.SetBasicAuth(user, pass)
	request.Header.Set("Accept", "application/json")
	response, err := (&http.Client{Timeout: 15 * time.Second}).Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		body := strings.TrimSpace(string(raw))
		if len(body) > 200 {
			body = body[:200]
		}
		return nil, fmt.Errorf("Bitbucket pull request list API returned %s: %s", response.Status, body)
	}
	return raw, nil
}

func statusSameOrigin(left, right string) bool {
	a, errA := url.Parse(left)
	b, errB := url.Parse(right)
	if errA != nil || errB != nil || a.Host == "" || b.Host == "" {
		return false
	}
	return a.Scheme == b.Scheme && strings.EqualFold(a.Host, b.Host)
}
