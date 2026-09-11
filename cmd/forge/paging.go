package main

// Pagination for list and resolver endpoints (allod/tools#89).
//
// Every one of these endpoints caps a page at 50 items no matter what limit
// is asked for: "forge -R allod/tools issue list -s all -L 500" against the
// real forge returned exactly 50 rows, numbers 109 through 188, and silently
// dropped everything below -- including all of the open issues. fetchPages is
// the one place that walks pages until it has everything a caller needs, so
// no list or resolver can again stop at the first page and call it the whole
// result set.

import "strconv"

// serverPageCap is the largest page the server hands back regardless of the
// limit requested.
const serverPageCap = 50

// fetchPages GETs basePath page by page and returns every array element
// collected across the pages it fetched. basePath carries whatever query the
// caller already built, "?" included if it built one, but no limit or page
// parameter of its own; fetchPages appends "&limit=<size>&page=<n>" (or
// "?limit=<size>&page=<n>" when basePath has no query yet), starting at
// page 1, so an existing query's parameters and their order survive
// untouched.
//
// limit is the caller's desired item count: 0 fetches every page there is.
// Each page is requested at serverPageCap items, or at limit when that is
// smaller. Fetching stops the moment a page comes back with fewer items than
// were requested -- an empty page included -- or once limit items have been
// collected, whichever comes first; the result is truncated to limit in the
// latter case. A failed request dies through api()'s existing error path,
// exactly as a single-page caller's request already did.
func fetchPages(basePath string, limit int) []any {
	size := serverPageCap
	if limit > 0 && limit < size {
		size = limit
	}

	sep := "&"
	if !queryStarted(basePath) {
		sep = "?"
	}

	var items []any
	for page := 1; ; page++ {
		path := basePath + sep + "limit=" + strconv.Itoa(size) + "&page=" + strconv.Itoa(page)
		got := jsonArray(mustJSON(api("GET", path, nil)))
		items = append(items, got...)
		if limit > 0 && len(items) >= limit {
			break
		}
		if len(got) < size {
			break
		}
	}
	if limit > 0 && len(items) > limit {
		items = items[:limit]
	}
	return items
}

// queryStarted reports whether basePath already carries a "?", so fetchPages
// knows whether to start its own added parameters with "?" or join them with
// "&".
func queryStarted(basePath string) bool {
	for i := 0; i < len(basePath); i++ {
		if basePath[i] == '?' {
			return true
		}
	}
	return false
}
