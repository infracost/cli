package eventstest

import "context"

type Event struct {
	Name  string
	Extra []interface{}
}

func (e Event) Field(key string) (interface{}, bool) {
	for i := 0; i+1 < len(e.Extra); i += 2 {
		if e.Extra[i] == key {
			return e.Extra[i+1], true
		}
	}
	return nil, false
}

type Recorder struct {
	Events []Event
}

func (r *Recorder) Push(_ context.Context, name string, extra ...interface{}) {
	r.Events = append(r.Events, Event{Name: name, Extra: extra})
}
