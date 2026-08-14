package article

import (
	"fmt"
	"log"
	"math/rand"
	"strings"
	"sync"
	"time"

	"google-automation/internal/config"
	"google-automation/internal/storage"
)

// SearchMethod describes how a search task should be constructed for an article.
type SearchMethod int

const (
	// MethodExactTitle searches by the exact article title.
	MethodExactTitle SearchMethod = iota
	// MethodMetaDesc searches by the full meta description.
	MethodMetaDesc
	// MethodPartialTitle searches by a partial title (first few words).
	MethodPartialTitle
	// MethodTitlePlusMeta combines title + partial meta desc.
	MethodTitlePlusMeta
	// MethodKeywordTitle searches by a keyword extracted from the title.
	MethodKeywordTitle
	// MethodKeywordMeta searches by a keyword extracted from the meta description.
	MethodKeywordMeta
)

func (m SearchMethod) String() string {
	switch m {
	case MethodExactTitle:
		return "exact_title"
	case MethodMetaDesc:
		return "meta_desc"
	case MethodPartialTitle:
		return "partial_title"
	case MethodTitlePlusMeta:
		return "title_plus_meta"
	case MethodKeywordTitle:
		return "keyword_title"
	case MethodKeywordMeta:
		return "keyword_meta"
	default:
		return "unknown"
	}
}

// SelectedArticle holds an article chosen for a search task plus the
// search method to use.
type SelectedArticle struct {
	Article      storage.Article
	SearchMethod SearchMethod
	Query        string
}

// Queue manages article selection with randomization and search-method variation.
type Queue struct {
	db        *storage.DB
	articles  []storage.Article
	collector *Collector
	extractor *Extractor
}

// NewQueue creates an article queue backed by the database.
func NewQueue(db *storage.DB) *Queue {
	return &Queue{
		db:        db,
		collector: NewCollector(),
		extractor: NewExtractor(),
	}
}

// RefreshArticles re-scrapes all configured domains for articles and persists
// them to the database. This is called on startup and at the configured interval.
// Extraction is parallelized with a semaphore (max 20 concurrent HTTP fetches).
func (q *Queue) RefreshArticles(domains []string) error {
	const maxConcurrent = 20

	for _, domain := range domains {
		log.Printf("[article-queue] collecting articles for %s", domain)
		urls, err := q.collector.Collect(domain)
		if err != nil {
			log.Printf("[article-queue] sitemap fetch failed for %s: %v", domain, err)
			continue
		}

		// Filter article URLs first.
		var articleURLs []SitemapURL
		for _, u := range urls {
			if IsArticleURL(u.Loc) {
				articleURLs = append(articleURLs, u)
			}
		}
		log.Printf("[article-queue] %s: found %d article URLs, extracting metadata (parallel)...", domain, len(articleURLs))

		type result struct {
			art *storage.Article
			err error
		}

		sem := make(chan struct{}, maxConcurrent)
		results := make(chan result, len(articleURLs))
		var wg sync.WaitGroup

		for _, u := range articleURLs {
			wg.Add(1)
			go func(loc string) {
				defer wg.Done()
				sem <- struct{}{}
				defer func() { <-sem }()

				extracted, err := q.extractor.Extract(loc)
				if err != nil {
					results <- result{err: fmt.Errorf("%s: %w", loc, err)}
					return
				}
				results <- result{art: &storage.Article{
					Domain:   domain,
					URL:      extracted.URL,
					Title:    extracted.Title,
					MetaDesc: extracted.MetaDesc,
					Topic:    extracted.Topic,
				}}
			}(u.Loc)
		}

		// Close results channel after all goroutines finish.
		go func() {
			wg.Wait()
			close(results)
		}()

		saved := 0
		for r := range results {
			if r.err != nil {
				log.Printf("[article-queue] extract failed: %v", r.err)
				continue
			}
			if _, err := q.db.UpsertArticle(r.art); err != nil {
				log.Printf("[article-queue] upsert failed for %s: %v", r.art.URL, err)
				continue
			}
			saved++
		}
		log.Printf("[article-queue] %s: saved %d articles", domain, saved)
	}

	// Reload the in-memory cache.
	return q.reload()
}

// reload fetches all articles from the DB into the in-memory cache.
func (q *Queue) reload() error {
	articles, err := q.db.AllArticles()
	if err != nil {
		return fmt.Errorf("load articles: %w", err)
	}
	q.articles = articles
	log.Printf("[article-queue] cached %d articles", len(q.articles))
	return nil
}

// LoadFromDB loads articles from the database without re-scraping.
func (q *Queue) LoadFromDB() error {
	return q.reload()
}

// PickRandom selects a random eligible article and assigns a random search method.
// Eligibility: searched_count < regularMax, OR (created within 7 days AND < newArticleBoost).
func (q *Queue) PickRandom(cfg *config.SchedulerConfig) (*SelectedArticle, bool) {
	if len(q.articles) == 0 {
		return nil, false
	}

	// Filter eligible articles.
	var eligible []storage.Article
	for _, a := range q.articles {
		if q.isEligible(a, cfg) {
			eligible = append(eligible, a)
		}
	}

	if len(eligible) == 0 {
		return nil, false
	}

	// Random article selection.
	article := eligible[rand.Intn(len(eligible))]

	// Random search method selection (prevents repetitive search patterns).
	method := SearchMethod(rand.Intn(int(MethodKeywordMeta) + 1))
	query := q.buildQuery(article, method)

	return &SelectedArticle{
		Article:      article,
		SearchMethod: method,
		Query:        query,
	}, true
}

// isEligible checks whether an article can still be searched today.
func (q *Queue) isEligible(a storage.Article, cfg *config.SchedulerConfig) bool {
	// New article boost: first 7 days allow up to newArticleBoost searches.
	isNew := false
	if !a.CreatedAt.IsZero() {
		isNew = time.Since(a.CreatedAt).Hours() < 168 // 7 days
	}
	if isNew && a.SearchedCount < cfg.NewArticleBoost {
		return true
	}
	// Regular cap.
	return a.SearchedCount < cfg.RegularMax
}

// buildQuery constructs the search query string based on the chosen method.
func (q *Queue) buildQuery(a storage.Article, method SearchMethod) string {
	switch method {
	case MethodExactTitle:
		return a.Title

	case MethodMetaDesc:
		if a.MetaDesc == "" {
			return a.Title
		}
		return a.MetaDesc

	case MethodPartialTitle:
		words := strings.Fields(a.Title)
		if len(words) <= 3 {
			return a.Title
		}
		return strings.Join(words[:3], " ")

	case MethodTitlePlusMeta:
		words := strings.Fields(a.MetaDesc)
		partial := ""
		if len(words) > 3 {
			partial = strings.Join(words[:3], " ")
		} else if len(words) > 0 {
			partial = strings.Join(words, " ")
		}
		if partial == "" {
			return a.Title
		}
		return a.Title + " " + partial

	case MethodKeywordTitle:
		// Extract the first significant word from the title.
		words := strings.Fields(a.Title)
		for _, w := range words {
			if len(w) > 3 {
				return w
			}
		}
		if len(words) > 0 {
			return words[0]
		}
		return a.Title

	case MethodKeywordMeta:
		// Extract the first significant word from the meta description.
		words := strings.Fields(a.MetaDesc)
		for _, w := range words {
			if len(w) > 3 {
				return w
			}
		}
		// Fallback to title keyword if meta is empty.
		titleWords := strings.Fields(a.Title)
		if len(titleWords) > 0 {
			return titleWords[0]
		}
		return a.Title

	default:
		return a.Title
	}
}

// GeneratePreSearchQueries produces 1-2 topically related queries that are NOT
// the exact article title. These simulate organic browsing behavior before the
// target search. The Python worker uses these for pre-search activity.
func (q *Queue) GeneratePreSearchQueries(a storage.Article) []string {
	var queries []string

	// Query 1: topic + domain (loose topical search).
	if a.Topic != "" {
		queries = append(queries, a.Topic)
	}

	// Query 2: a keyword from the title (different from the exact title).
	words := strings.Fields(a.Title)
	if len(words) > 2 {
		// Pick a middle word (less obvious than the first).
		mid := len(words) / 2
		queries = append(queries, words[mid])
	}

	// Ensure at least one pre-search query.
	if len(queries) == 0 && a.Title != "" {
		queries = append(queries, strings.Fields(a.Title)[0])
	}

	return queries
}

// Count returns the number of articles in the in-memory cache.
func (q *Queue) Count() int {
	return len(q.articles)
}
