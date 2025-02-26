package humanlog

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

	typesv1 "github.com/humanlogio/api/go/types/v1"
	"google.golang.org/protobuf/types/known/timestamppb"
)

// JSONHandler can handle logs emitted by logrus.TextFormatter loggers.
type JSONHandler struct {
	Opts *HandlerOptions

	Level   string
	Time    time.Time
	Message string
	Fields  map[string]string
}

// searchJSON searches a document for a key using the found func to determine if the value is accepted.
// kvs is the deserialized json document.
// fieldList is a list of field names that should be searched. Sub-documents can be searched by using the dot (.). For example, to search {"data"{"message": "<this field>"}} the item would be data.message
func searchJSON(kvs map[string]interface{}, fieldList []string, found func(key string, value interface{}) bool) bool {
	for _, field := range fieldList {
		splits := strings.SplitN(field, ".", 2)
		if len(splits) > 1 {
			name, fieldKey := splits[0], splits[1]
			val, ok := kvs[name]
			if !ok {
				// the key does not exist in the document
				continue
			}
			if m, ok := val.(map[string]interface{}); ok {
				// its value is JSON and was unmarshaled to map[string]interface{} so search the sub document
				return searchJSON(m, []string{fieldKey}, found)
			}
		} else {
			// this is not a sub-document search, so search the root
			for k, v := range kvs {
				if field == k && found(k, v) {
					return true
				}
			}
		}
	}
	return false
}

func (h *JSONHandler) clear() {
	h.Level = ""
	h.Time = time.Time{}
	h.Message = ""
	h.Fields = make(map[string]string)
}

// TryHandle tells if this line was handled by this handler.
func (h *JSONHandler) TryHandle(d []byte, out *typesv1.StructuredLogEvent) bool {
	h.clear()
	if !h.UnmarshalJSON(d) {
		return false
	}
	out.Timestamp = timestamppb.New(h.Time)
	out.Msg = h.Message
	out.Lvl = h.Level
	for k, v := range h.Fields {
		out.Kvs = append(out.Kvs, &typesv1.KV{Key: k, Value: v})
	}
	return true
}

func deleteJSONKey(key string, jsonData map[string]interface{}) {
	if _, ok := jsonData[key]; ok {
		// found the key at the root
		delete(jsonData, key)
		return
	}

	splits := strings.SplitN(key, ".", 2)
	if len(splits) < 2 {
		// invalid selector
		return
	}
	k, v := splits[0], splits[1]
	ifce, ok := jsonData[k]
	if !ok {
		return // the key doesn't exist
	}
	if m, ok := ifce.(map[string]interface{}); ok {
		deleteJSONKey(v, m)
	}
}

func getFlattenedFields(v map[string]interface{}) map[string]string {
	extValues := make(map[string]string)
	for key, nestedVal := range v { 
		switch valTyped := nestedVal.(type) {
		case float64:
			if valTyped-math.Floor(valTyped) < 0.000001 && valTyped < 1e9 {
				extValues[key] = fmt.Sprintf("%d", int(valTyped))
			} else {
				extValues[key] = fmt.Sprintf("%g", valTyped)
			}
		case string:
			extValues[key] = fmt.Sprintf("%q", valTyped)
		case map[string]interface{}:
			flattenedFields := getFlattenedFields(valTyped)
			for keyNested, valStr := range flattenedFields { 
				extValues[key + "." + keyNested] = valStr
			}
		default:
			extValues[key] = fmt.Sprintf("%v", valTyped)
		}
	} 
	return extValues
}

// UnmarshalJSON sets the fields of the handler.
func (h *JSONHandler) UnmarshalJSON(data []byte) bool {
	raw := make(map[string]interface{})
	err := json.Unmarshal(data, &raw)
	if err != nil {
		return false
	}

	searchJSON(raw, h.Opts.TimeFields, func(field string, value interface{}) bool {
		var ok bool
		h.Time, ok = tryParseTime(value)
		if ok {
			deleteJSONKey(field, raw)
		}
		return ok
	})

	searchJSON(raw, h.Opts.MessageFields, func(field string, value interface{}) bool {
		var ok bool
		h.Message, ok = value.(string)
		if ok {
			deleteJSONKey(field, raw)
		}
		return ok
	})

	searchJSON(raw, h.Opts.LevelFields, func(field string, value interface{}) bool {
		if strLvl, ok := value.(string); ok {
			h.Level = strLvl
			deleteJSONKey(field, raw)
		} else if flLvl, ok := value.(float64); ok {
			h.Level = convertBunyanLogLevel(flLvl)
			deleteJSONKey(field, raw)
		} else {
			h.Level = "???"
		}
		return true
	})

	if h.Fields == nil {
		h.Fields = make(map[string]string)
	}

	for key, val := range raw {
		switch v := val.(type) {
		case float64:
			if v-math.Floor(v) < 0.000001 && v < 1e9 {
				// looks like an integer that's not too large
				h.Fields[key] = fmt.Sprintf("%d", int(v))
			} else {
				h.Fields[key] = fmt.Sprintf("%g", v)
			}
		case string:
			h.Fields[key] = fmt.Sprintf("%q", v)
		case map[string]interface{}:
			flattenedFields := getFlattenedFields(v)
			for keyNested, val := range flattenedFields { 
				h.Fields[key + "." + keyNested] = fmt.Sprintf("%v", val)
			}
		default:
			h.Fields[key] = fmt.Sprintf("%v", v)
		}
	}

	return true
}

// convertBunyanLogLevel returns a human readable log level given a numerical bunyan level
// https://github.com/trentm/node-bunyan#levels
func convertBunyanLogLevel(level float64) string {
	switch level {
	case 10:
		return "trace"
	case 20:
		return "debug"
	case 30:
		return "info"
	case 40:
		return "warn"
	case 50:
		return "error"
	case 60:
		return "fatal"
	default:
		return "???"
	}
}
