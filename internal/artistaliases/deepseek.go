package artistaliases

import "github.com/lux032/032music-server/internal/websearch"

// Type aliases unifying artistaliases with the shared internal/websearch layer
// so all callers share the same Tavily/DeepSeek router and error taxonomy.
type (
	WebError     = websearch.WebError
	SearchResult = websearch.SearchResult
	Web          = websearch.Client
	DeepSeekWeb  = websearch.DeepSeekClient
)
