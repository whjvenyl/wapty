package mocksy

import (
	"bytes"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"sort"
	"strings"
)

// FIXME: use a container better suited for searching. Must find an efficient key
// to do fuzzy search with requests.
type responseDB []Item

var responseHistory responseDB

func init() {
	responseHistory = make([]Item, 0)
}

// AddToHistory inserts a pair request-response in the responseHistory.
func AddToHistory(itm Item) {
	responseHistory = append(responseHistory, itm)
}

func HistoryLength() int {
	return len(responseHistory)
}

// FindMatching takes an http request and returns the closest match to it
// based on the response history.

func FindMatching(req *http.Request) string {
	host := findHost(req)
	// Take only requests matching our filter criteria and sort them by best match
	viableReqs := filterByHost(responseHistory, host)
	fuzzySort(viableReqs, host, req)

	return ""
}

// findHost tries to retreive host information from `req`.
// It fills Host.Value with the verbatim req.Host string, then tries to
// find the correct Ip as well from header information.
func findHost(req *http.Request) Host {
	host := Host{
		Value: req.Host,
		Ip:    "",
	}
	if ip := req.Header.Get("X-Forwarded-For"); len(ip) > 0 {
		host.Ip = ip
	} else {
		host.Ip = req.RemoteAddr
		if id := strings.Index(host.Ip, ":"); id > -1 {
			host.Ip = host.Ip[:id]
		}
	}
	return host
}

// filterByHost returns all elements in `lst` whose host is `host` (matching either by value or by ip)
func filterByHost(lst []Item, host Host) []Item {
	newlst := make([]Item, 0)
	for _, e := range lst {
		if e.Host.Value == host.Value || e.Host.Ip == host.Ip {
			newlst = append(newlst, e)
		}
	}
	return newlst
}

// compareArgs is a struct containing the information that we use to match
// two requests.
type compareArgs struct {
	Request  []byte // XXX: []byte or Request?
	Host     Host
	Port     string
	Protocol string
	Method   string
	Path     string
}

// fuzzySort sorts the requests from the "best matching" with `req` to the least.
// Sort is done in place, so the given `reqs` is modified by this call.
func fuzzySort(reqs []Item, host Host, req *http.Request) {
	if len(reqs) == 0 {
		return
	}
	body, err := ioutil.ReadAll(req.Body)
	if err != nil {
		fmt.Fprintf(os.Stderr, "mocksy: error reading body of request while sorting: %s\n", err.Error())
		return
	}
	less := fuzzyComparer(reqs, compareArgs{
		Request:  body,
		Host:     host,
		Port:     req.URL.Port(),
		Protocol: req.Proto,
		Method:   req.Method,
		Path:     req.URL.EscapedPath(),
	})
	sort.Slice(reqs, less)
}

// fuzzyComparer returns a `Less` function which, given requests i and j,
// tells which one matches the given `args` the most.
// This is the most important part of Mocksy, as the quality of the matches
// depends on the returned comparer.
func fuzzyComparer(reqs []Item, args compareArgs) func(int, int) bool {
	// longestPrefix returns the number of common runes at the beginning of
	// strings `a` and `b`. For convenience, it also returns whether the strings
	// are the same or not.
	longestPrefix := func(a, b string) (pfx int, perfectMatch bool) {
		if perfectMatch = a == b; perfectMatch {
			return
		}
		for i := 0; i < len(a) && i < len(b); i++ {
			if a[i] != b[i] {
				break
			}
			pfx++
		}
		return
	}
	return func(i, j int) bool {
		ra, rb := reqs[i], reqs[j]
		// First, check path. If one of the paths is the same as the original one
		// and the other's not, it's the best candidate.
		_, perfectMatchA := longestPrefix(ra.Path, args.Path)
		_, perfectMatchB := longestPrefix(rb.Path, args.Path)
		if perfectMatchA != perfectMatchB {
			// Here, the boolean value of `perfectMatchA` means "ra matches exactly, and rb does not".
			// In that case, ra is a better candidate and should be considered "less" than rb
			// (since we order best-first). Else, rb is the better candidate.
			return perfectMatchA
		}

		// Here, either both paths match exactly, or neither does.
		// In this case, we check the request.
		reqAExact := bytes.Equal(ra.Request.Value, args.Request)
		reqBExact := bytes.Equal(rb.Request.Value, args.Request)
		if reqAExact != reqBExact {
			// If one of the requests matches exactly and the other does not, we have our decision.
			return reqAExact
		}

		// Else, get the information on which request is closer to the actual one.
		// TODO: for now, we just check the _length_ of the requests, not the content
		var aMatchesMost bool
		//var minReqLenDiff = 0
		{
			diffLenA := len(ra.Request.Value) - len(args.Request)
			diffLenB := len(rb.Request.Value) - len(args.Request)
			if diffLenA < 0 {
				diffLenA = -diffLenA
			}
			if diffLenB < 0 {
				diffLenB = -diffLenB
			}
			aMatchesMost = diffLenA < diffLenB
			//if aMatchesMost {
			//minReqLenDiff = diffLenA
			//} else {
			//minReqLenDiff = diffLenB
			//}
		}

		// Now check the method. If one of the methods matches and the other does not,
		// it's considered the best candidate unless the other's request is closer
		// to the actual one. In that case, use heuristic to decide the better option.
		if (ra.Method == args.Method) != (rb.Method == args.Method) {

			// In this case, one of the methods matches exactly and the other does not.

			if (ra.Method == args.Method) != aMatchesMost {
				// In this case, one of the requests has the same method, but the other has
				// a request body which matches more the original one.
				// For now, we just prefer the method over the request, but here we may use
				// heuristics (like `minReqLenDiff`) to have better control over this choice.
				return ra.Method == args.Method
			} else {
				// Here, a request matches the actual method _and_ its request body is
				// closer to the original one. Return that request without further investigation.
				return ra.Method == args.Method
			}
		}

		// Here, either both methods match or neither does.
		// Check the protocol.
		if (ra.Protocol == args.Protocol) != (rb.Protocol == args.Protocol) {
			// One of the protocol matches, the other does not.
			// Like before, we may use heuristics on the request bodies to determine our choice,
			// but for now just return the request whose protocol matches.
			return ra.Protocol == args.Protocol
		}

		// Finally, check port.
		if (ra.Port == args.Port) != (rb.Port == args.Port) {
			return ra.Port == args.Port
		}

		// If we got here, all previous criteria failed and the requests are almost the same.
		// In this case, return the one whose request body is closer to the original.
		return aMatchesMost
	}
}