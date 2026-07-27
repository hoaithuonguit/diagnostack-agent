// Package yamlcfg is a minimal YAML parser for the diagnostack agent config file.
// It handles only the subset of YAML used by config.yaml:
//   - Simple key: value pairs
//   - Nested sections (one level)
//   - go time.Duration strings (e.g. "15s", "1m")
//   - int, string, and duration values
//
// We implement this ourselves to avoid adding external dependencies
// and to keep the agent binary minimal.
package config

import (
	"bufio"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// parseYAML reads the config.yaml format into a flat map of dotted keys.
// For example:
//
//	redis:
//	  addr: "localhost:6379"
//
// becomes {"redis.addr": "localhost:6379"}.
func parseYAML(r io.Reader) (map[string]string, error) {
	result := make(map[string]string, 32)
	var section string

	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		line := scanner.Text()

		// Strip inline comments.
		if idx := strings.Index(line, " #"); idx >= 0 {
			line = line[:idx]
		}

		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}

		// Detect section headers (no leading spaces, ends with colon, no value).
		if !strings.HasPrefix(line, " ") && !strings.HasPrefix(line, "\t") {
			k, v, found := strings.Cut(trimmed, ":")
			if !found {
				continue
			}
			v = strings.TrimSpace(v)
			v = strings.Trim(v, `"'`)
			if v == "" {
				// This is a section header.
				section = strings.TrimSpace(k)
				continue
			}
			// Top-level key: value.
			section = ""
			result[strings.TrimSpace(k)] = v
			continue
		}

		// Indented key: value under current section.
		k, v, found := strings.Cut(trimmed, ":")
		if !found {
			continue
		}
		v = strings.TrimSpace(v)
		v = strings.Trim(v, `"'`)
		key := strings.TrimSpace(k)
		if section != "" {
			key = section + "." + key
		}
		result[key] = v
	}

	return result, scanner.Err()
}

// applyParsed maps the flat key map back onto the File struct.
func applyParsed(f *File, kv map[string]string) error {
	for key, val := range kv {
		if val == "" {
			continue
		}
		var err error
		switch key {
		case "agent_id":
			f.AgentID = val
		case "server_label":
			f.ServerLabel = val

		case "redis.addr":
			f.Redis.Addr = val
		case "redis.password":
			f.Redis.Password = val
		case "redis.db":
			f.Redis.DB, err = strconv.Atoi(val)

		case "api.endpoint":
			f.API.Endpoint = val
		case "api.api_key":
			f.API.APIKey = val
		case "api.timeout":
			f.API.Timeout, err = time.ParseDuration(val)

		case "collect_interval":
			f.CollectInterval, err = time.ParseDuration(val)
		case "collect_timeout":
			f.CollectTimeout, err = time.ParseDuration(val)
		case "buffer_capacity":
			f.BufferCapacity, err = strconv.Atoi(val)

		case "retry.max_attempts":
			f.Retry.MaxAttempts, err = strconv.Atoi(val)
		case "retry.base_delay":
			f.Retry.BaseDelay, err = time.ParseDuration(val)
		case "retry.max_delay":
			f.Retry.MaxDelay, err = time.ParseDuration(val)

		case "log.level":
			f.Log.Level = val
		case "log.format":
			f.Log.Format = val
		}

		if err != nil {
			return fmt.Errorf("config key %q: %w", key, err)
		}
	}
	return nil
}
