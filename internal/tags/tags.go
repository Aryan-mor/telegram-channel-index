// Package tags is the fixed list of channel tags. Adding one takes a new release.
package tags

import "strings"

// All is the tag slugs a person can type, in display order.
var All = []string{
	"movie", "series", "anime", "documentary", "music", "joke",
	"meme", "news", "direct-file", "software", "education", "book",
}

// oldSlug is a tag this program used to store. It is read as its current slug.
var oldSlug = map[string]string{"film": "movie"}

// Canon returns the stored slug for raw, or empty when it is not a tag.
func Canon(raw string) string {
	raw = strings.ToLower(strings.TrimPrefix(strings.TrimSpace(raw), "#"))
	if next, ok := oldSlug[raw]; ok {
		raw = next
	}
	for _, t := range All {
		if raw == t {
			return t
		}
	}
	return ""
}
