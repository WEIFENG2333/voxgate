package asr

import "strings"

type SegmentResetAggregator struct {
	confirmed       string
	lastSegmentText string
	finalText       string
	segmentIndex    int
}

func (a *SegmentResetAggregator) Update(text string) (full string, reset bool, completed Segment) {
	if a.lastSegmentText != "" && len([]rune(text)) < len([]rune(a.lastSegmentText))/2 && !strings.HasPrefix(a.lastSegmentText, text) {
		completed = Segment{Index: a.segmentIndex, Text: a.lastSegmentText}
		a.segmentIndex++
		a.confirmed += a.lastSegmentText
		reset = true
	}
	a.lastSegmentText = text
	if a.confirmed == "" {
		full = text
	} else {
		full = a.confirmed + text
	}
	return full, reset, completed
}

func (a *SegmentResetAggregator) Final(text string) string {
	full, _, _ := a.Update(text)
	a.finalText = full
	return full
}

func (a *SegmentResetAggregator) Text() string {
	if a.finalText != "" {
		return a.finalText
	}
	if a.confirmed == "" {
		return a.lastSegmentText
	}
	return a.confirmed + a.lastSegmentText
}

func LongestOverlap(tail, head string) int {
	max := len(tail)
	if len(head) < max {
		max = len(head)
	}
	for k := max; k > 0; k-- {
		if tail[len(tail)-k:] == head[:k] {
			return k
		}
	}
	return 0
}
