package main

import (
	"os"
	"strconv"
	"time"

	"github.com/odvcencio/gotreesitter/grammars"
)

const defaultGrammarCacheLimit = 16
const defaultGrammarIdleTTL = time.Minute
const defaultGrammarIdleSweep = 15 * time.Second

func configureGrammarCache() {
	limit, err := strconv.Atoi(os.Getenv("GOTREESITTER_GRAMMAR_CACHE_LIMIT"))
	if err != nil {
		limit = defaultGrammarCacheLimit
	}
	grammars.SetEmbeddedLanguageCacheLimit(limit)

	sweep, err := time.ParseDuration(os.Getenv("GOTREESITTER_GRAMMAR_IDLE_SWEEP"))
	if err != nil {
		sweep = defaultGrammarIdleSweep
	}
	grammars.SetEmbeddedLanguageIdleSweepInterval(sweep)

	ttl, err := time.ParseDuration(os.Getenv("GOTREESITTER_GRAMMAR_IDLE_TTL"))
	if err != nil {
		ttl = defaultGrammarIdleTTL
	}
	grammars.SetEmbeddedLanguageIdleTTL(ttl)
}
