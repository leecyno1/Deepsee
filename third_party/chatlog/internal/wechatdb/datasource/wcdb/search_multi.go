package wcdb

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sjzar/chatlog/internal/model"
)

func normalizeDatabaseSearchTerms(keyword, match string) (string, []string, error) {
	keyword = strings.TrimSpace(keyword)
	if keyword == "" {
		return "", nil, fmt.Errorf("keyword is empty")
	}
	match = strings.ToLower(strings.TrimSpace(match))
	if match == "" {
		match = "phrase"
	}
	if match != "phrase" && match != "all" && match != "any" {
		return "", nil, fmt.Errorf("invalid database search match mode")
	}
	if match == "phrase" {
		return match, []string{keyword}, nil
	}
	fields := strings.Fields(keyword)
	terms := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, term := range fields {
		key := strings.ToLower(strings.TrimSpace(term))
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		terms = append(terms, strings.TrimSpace(term))
	}
	if len(terms) == 0 {
		return "", nil, fmt.Errorf("database search has no terms")
	}
	if len(terms) > 8 {
		terms = terms[:8]
	}
	return match, terms, nil
}

func (ds *DataSource) searchAllMultiTerm(
	ctx context.Context,
	request model.DatabaseSearchRequest,
	matchMode string,
	terms []string,
	sortMode string,
) (model.DatabaseSearchResult, error) {
	startedAt := time.Now()
	limit := request.Limit
	if limit <= 0 {
		limit = 100
	}
	if limit > 500 {
		limit = 500
	}
	offset := request.Offset
	if offset < 0 {
		offset = 0
	}
	candidateLimit := (limit + offset) * 5
	if candidateLimit < 50 {
		candidateLimit = 50
	}
	if candidateLimit > 500 {
		candidateLimit = 500
	}
	type aggregate struct {
		item  map[string]interface{}
		hits  int
		terms []string
	}
	combined := make(map[string]*aggregate)
	result := model.DatabaseSearchResult{
		Items: make([]map[string]interface{}, 0),
		Stats: model.DatabaseSearchStats{
			Path:   "multi_term_merge",
			Errors: make([]model.DatabaseSearchError, 0),
		},
	}
	for _, term := range terms {
		if err := ctx.Err(); err != nil {
			result.Stats.Partial = true
			break
		}
		child := request
		child.Keyword = term
		child.Match = "phrase"
		child.Sort = "source"
		child.Offset = 0
		child.Limit = candidateLimit
		termResult, err := ds.SearchAllDetailed(ctx, child)
		if err != nil {
			return result, err
		}
		result.Stats.FilesScanned += termResult.Stats.FilesScanned
		result.Stats.TablesScanned += termResult.Stats.TablesScanned
		result.Stats.FTSTables += termResult.Stats.FTSTables
		result.Stats.Partial = result.Stats.Partial || termResult.Stats.Partial
		result.Stats.CandidateCapped = result.Stats.CandidateCapped || termResult.Stats.CandidateCapped
		result.Stats.Errors = append(result.Stats.Errors, termResult.Stats.Errors...)
		seen := make(map[string]struct{}, len(termResult.Items))
		for _, item := range termResult.Items {
			key := databaseSearchHitKey(item)
			if _, exists := seen[key]; exists {
				continue
			}
			seen[key] = struct{}{}
			entry := combined[key]
			if entry == nil {
				copyItem := make(map[string]interface{}, len(item)+3)
				for name, value := range item {
					copyItem[name] = value
				}
				entry = &aggregate{item: copyItem}
				combined[key] = entry
			}
			entry.hits++
			entry.terms = append(entry.terms, term)
		}
	}
	all := make([]map[string]interface{}, 0, len(combined))
	for _, entry := range combined {
		if matchMode == "all" && entry.hits != len(terms) {
			continue
		}
		entry.item["relevance"] = entry.hits
		entry.item["matched_terms"] = append([]string(nil), entry.terms...)
		decorateDatabaseSearchHit(entry.item, entry.terms)
		all = append(all, entry.item)
	}
	if sortMode == "relevance" {
		sort.SliceStable(all, func(i, j int) bool {
			return databaseSearchHitBefore(all[i], all[j])
		})
	} else {
		sort.SliceStable(all, func(i, j int) bool {
			return databaseSearchHitKey(all[i]) < databaseSearchHitKey(all[j])
		})
	}
	result.Total = len(all)
	if offset < len(all) {
		end := offset + limit
		if end > len(all) {
			end = len(all)
		}
		result.Items = all[offset:end]
	}
	result.Stats.DurationMS = float64(time.Since(startedAt).Microseconds()) / 1000
	return result, nil
}

func databaseSearchHitKey(item map[string]interface{}) string {
	return strings.Join([]string{
		toString(item["group"]),
		toString(item["file"]),
		toString(item["table"]),
		toString(item["column"]),
		fmt.Sprint(item["row_id"]),
	}, "\x00")
}

func decorateDatabaseSearchHit(item map[string]interface{}, terms []string) {
	preview := toString(item["preview"])
	lower := strings.ToLower(preview)
	matched := make([]string, 0, len(terms))
	ranges := make([]map[string]interface{}, 0, len(terms))
	for _, term := range terms {
		byteIndex := strings.Index(lower, strings.ToLower(term))
		if byteIndex < 0 {
			continue
		}
		start := utf8.RuneCountInString(preview[:byteIndex])
		matched = append(matched, term)
		ranges = append(ranges, map[string]interface{}{
			"start": start,
			"end":   start + utf8.RuneCountInString(term),
			"term":  term,
		})
	}
	if _, exists := item["relevance"]; !exists {
		item["relevance"] = len(matched)
	}
	item["matched_terms"] = matched
	item["highlights"] = ranges
}

func databaseSearchHitBefore(left, right map[string]interface{}) bool {
	leftScore := int(toInt64(left["relevance"]))
	rightScore := int(toInt64(right["relevance"]))
	if leftScore != rightScore {
		return leftScore > rightScore
	}
	return databaseSearchHitKey(left) < databaseSearchHitKey(right)
}
