/*
 *
 * k6 - a next-generation load testing tool
 * Copyright (C) 2019 Load Impact
 *
 * This program is free software: you can redistribute it and/or modify
 * it under the terms of the GNU Affero General Public License as
 * published by the Free Software Foundation, either version 3 of the
 * License, or (at your option) any later version.
 *
 * This program is distributed in the hope that it will be useful,
 * but WITHOUT ANY WARRANTY; without even the implied warranty of
 * MERCHANTABILITY or FITNESS FOR A PARTICULAR PURPOSE.  See the
 * GNU Affero General Public License for more details.
 *
 * You should have received a copy of the GNU Affero General Public License
 * along with this program.  If not, see <http://www.gnu.org/licenses/>.
 *
 */

package statsd

import (
	"bytes"
	"encoding/json"
	"time"

	"github.com/kelseyhightower/envconfig"
	"gopkg.in/guregu/null.v3"

	"github.com/loadimpact/k6/lib/types"
	"github.com/loadimpact/k6/stats"
)

// config defines the StatsD configuration.
type config struct {
	Addr         null.String        `json:"addr,omitempty" envconfig:"K6_STATSD_ADDR"`
	BufferSize   null.Int           `json:"bufferSize,omitempty" envconfig:"K6_STATSD_BUFFER_SIZE"`
	Namespace    null.String        `json:"namespace,omitempty" envconfig:"K6_STATSD_NAMESPACE"`
	PushInterval types.NullDuration `json:"pushInterval,omitempty" envconfig:"K6_STATSD_PUSH_INTERVAL"`
	TagBlocklist stats.TagSet       `json:"tagBlocklist,omitempty" envconfig:"K6_STATSD_TAG_BLOCKLIST"`
	TagFormat    nullTagFormatType  `json:"tagFormat,omitempty" envconfig:"K6_STATSD_TAG_FORMAT"`
}

//go:generate enumer -type=tagFormatType -trimprefix tagFormat -transform snake -output tag_format_gen.go

// tagFormatType is "enum" type representing the different tag formats the statsd output support
type tagFormatType int

const (
	tagFormatNone tagFormatType = iota
	tagFormatDatadog
	tagFormatNewrelic
	tagFormatStatsdv2
)

func processTags(t stats.TagSet, tags map[string]string) []string {
	var res []string
	for key, value := range tags {
		if value != "" && !t[key] {
			res = append(res, key+":"+value)
		}
	}
	return res
}

// nullTagFormatType implements the null type around TagFormatType
type nullTagFormatType struct {
	t     tagFormatType
	Valid bool
}

// UnmarshalText converts text data to a valid NullTagFormatType
func (d *nullTagFormatType) UnmarshalText(data []byte) error {
	if len(data) == 0 {
		*d = nullTagFormatType{}
		return nil
	}
	var err error
	d.t, err = tagFormatTypeString(string(data))

	if err != nil {
		return err
	}
	d.Valid = true
	return nil
}

// UnmarshalJSON converts JSON data to a valid NullTagFormatType
func (d *nullTagFormatType) UnmarshalJSON(data []byte) error {
	if bytes.Equal(data, []byte(`null`)) {
		d.Valid = false
		return nil
	}

	var s string
	var err error
	if err = json.Unmarshal(data, &s); err != nil {
		return err
	}
	d.t, err = tagFormatTypeString(s)

	if err != nil {
		return err
	}

	d.Valid = true
	return nil
}

// MarshalJSON returns the JSON representation of d
func (d nullTagFormatType) MarshalJSON() ([]byte, error) {
	if !d.Valid {
		return []byte(`null`), nil
	}
	return json.Marshal(d.t)
}

// Apply saves config non-zero config values from the passed config in the receiver.
func (c config) Apply(cfg config) config {
	if cfg.Addr.Valid {
		c.Addr = cfg.Addr
	}
	if cfg.BufferSize.Valid {
		c.BufferSize = cfg.BufferSize
	}
	if cfg.Namespace.Valid {
		c.Namespace = cfg.Namespace
	}
	if cfg.PushInterval.Valid {
		c.PushInterval = cfg.PushInterval
	}
	if cfg.TagBlocklist != nil {
		c.TagBlocklist = cfg.TagBlocklist
	}
	if cfg.TagFormat.Valid {
		c.TagFormat = cfg.TagFormat
	}

	return c
}

// newConfig creates a new Config instance with default values for some fields.
func newConfig() config {
	return config{
		Addr:         null.NewString("localhost:8125", false),
		BufferSize:   null.NewInt(20, false),
		Namespace:    null.NewString("k6.", false),
		PushInterval: types.NewNullDuration(1*time.Second, false),
		TagBlocklist: stats.TagSet{},
		TagFormat:    nullTagFormatType{t: tagFormatNone},
	}
}

// getConsolidatedConfig combines {default config values + JSON config +
// environment vars}, and returns the final result.
func getConsolidatedConfig(jsonRawConf json.RawMessage, env map[string]string, _ string) (config, error) {
	result := newConfig()
	if jsonRawConf != nil {
		jsonConf := config{}
		if err := json.Unmarshal(jsonRawConf, &jsonConf); err != nil {
			return result, err
		}
		result = result.Apply(jsonConf)
	}

	envConfig := config{}
	_ = env // TODO: get rid of envconfig and actually use the env parameter...
	if err := envconfig.Process("", &envConfig); err != nil {
		return result, err
	}
	result = result.Apply(envConfig)

	return result, nil
}
