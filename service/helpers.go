package service

import (
	"encoding/json"

	"github.com/jrniemiec/arc/store"
)

func parseEventLine(line string, e *store.Event) error {
	return json.Unmarshal([]byte(line), e)
}
