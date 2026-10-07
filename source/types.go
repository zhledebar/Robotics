package main

import (
	"encoding/json"
	"time"
)

const version = "V8.24"
const stockIn = "有货"
const stockOut = "缺货"
const stockUnknown = "未确认"

type DellResult struct {
	OfferID           string            `json:"offer_id"`
	Source            string            `json:"source"`
	CPU               string            `json:"cpu"`
	GPU               string            `json:"gpu"`
	Memory            string            `json:"memory"`
	Storage           string            `json:"storage"`
	Display           string            `json:"display"`
	URL               string            `json:"url"`
	Price             float64           `json:"price"`
	Original          float64           `json:"original"`
	Discount          float64           `json:"discount"`
	Stock             string            `json:"stock"`
	Matched           bool              `json:"matched"`
	Confirmed         bool              `json:"confirmed"`
	Custom            bool              `json:"custom,omitempty"`
	DiscountConfirmed bool              `json:"discount_confirmed,omitempty"`
	ConfigUnbound     bool              `json:"config_unbound,omitempty"`
	Stale             bool              `json:"stale,omitempty"`
	Note              string            `json:"note,omitempty"`
	Options           map[string]string `json:"options,omitempty"`
}

type HistoryPoint struct {
	CheckedAt         string  `json:"checked_at"`
	Price             float64 `json:"price"`
	Original          float64 `json:"original"`
	Discount          float64 `json:"discount"`
	Stock             string  `json:"stock"`
	Changed           bool    `json:"changed"`
	Note              string  `json:"note"`
	Custom            bool    `json:"custom,omitempty"`
	DiscountConfirmed bool    `json:"discount_confirmed,omitempty"`
}

type Product struct {
	BrowserFeed bool `json:"browser_feed"` // retained only to migrate the rejected V8.0 workflow

	ID                      string                     `json:"id"`
	Name                    string                     `json:"name"`
	URL                     string                     `json:"url"`
	IntervalMin             float64                    `json:"interval_min"`
	TargetPrice             float64                    `json:"target_price"`
	MinDiscount             float64                    `json:"min_discount"`
	CustomDiscountOnly      bool                       `json:"custom_discount_only"`
	AlertOnDiscountIncrease bool                       `json:"alert_on_discount_increase"`
	CooldownMin             float64                    `json:"cooldown_min"`
	AlertOnRestock          bool                       `json:"alert_on_restock"`
	AlertOnPriceDrop        bool                       `json:"alert_on_price_drop"`
	Active                  bool                       `json:"active"`
	DellFamilyScan          bool                       `json:"dell_family_scan"`
	LastPrice               float64                    `json:"last_price"`
	LastOriginal            float64                    `json:"last_original"`
	LastDiscount            float64                    `json:"last_discount"`
	LastStock               string                     `json:"last_stock"`
	LastError               string                     `json:"last_error"`
	LastParser              string                     `json:"last_parser"`
	LastSuccess             string                     `json:"last_success"`
	LastChecked             string                     `json:"last_checked"`
	LastAlertAt             string                     `json:"last_alert_at"`
	LastSignature           string                     `json:"last_signature,omitempty"`
	History                 []HistoryPoint             `json:"history,omitempty"`
	DellResults             []DellResult               `json:"dell_results,omitempty"`
	LenovoResults           []DellResult               `json:"lenovo_results,omitempty"`
	LastTrusted             bool                       `json:"last_trusted"`
	Stale                   bool                       `json:"stale"`
	Checking                bool                       `json:"checking"`
	CheckState              string                     `json:"check_state"`
	CheckStarted            string                     `json:"check_started,omitempty"`
	CheckSeq                uint64                     `json:"check_seq"`
	NeedsAction             string                     `json:"needs_action,omitempty"`
	ScanIncomplete          bool                       `json:"scan_incomplete"`
	Extra                   map[string]json.RawMessage `json:"-"`
	revision                uint64
	nextCheck               time.Time
}

// Keep fields introduced by earlier versions, even if this version does not use them.
func (p *Product) UnmarshalJSON(b []byte) error {
	type plain Product
	var v plain
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	*p = Product(v)
	var all, known map[string]json.RawMessage
	_ = json.Unmarshal(b, &all)
	x, _ := json.Marshal(v)
	_ = json.Unmarshal(x, &known)
	for k := range known {
		delete(all, k)
	}
	p.Extra = all
	return nil
}
func (p Product) MarshalJSON() ([]byte, error) {
	type plain Product
	b, err := json.Marshal(plain(p))
	if err != nil {
		return nil, err
	}
	var v map[string]json.RawMessage
	_ = json.Unmarshal(b, &v)
	for k, x := range p.Extra {
		if _, ok := v[k]; !ok {
			v[k] = x
		}
	}
	return json.Marshal(v)
}

type Settings struct {
	GlobalPaused bool `json:"global_paused"`
}
type Store struct {
	Products []*Product `json:"products"`
	Settings Settings   `json:"settings"`
	Schema   int        `json:"schema,omitempty"`
}
type Alert struct {
	ID           string `json:"id"`
	ProductID    string `json:"product_id"`
	Name         string `json:"name"`
	Message      string `json:"message"`
	Created      string `json:"created"`
	URL          string `json:"url"`
	Acknowledged bool   `json:"acknowledged"`
}
type Observation struct {
	VerifiedNative bool

	Price    float64
	Original float64
	Discount float64
	Stock    string
	Parser   string
	Results  []DellResult
	Partial  bool
	Note     string
}

func stamp() string { return time.Now().Format("2006-01-02 15:04:05") }
