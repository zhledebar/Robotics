package main

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log"
	"math"
)

//go:embed owned_confirm.js
var ownedConfirmScript string

func ownedConfirmExpression(r nativeRequest) string {
	b, _ := json.Marshal(r)
	return "(" + ownedConfirmScript + ")(" + string(b) + ")"
}

// Only a modal linked by Dell's data-id to the requested option/product may
// be accepted. Dispatching confirmation does not prove the requested selection.
func ownedDOMConfirmation(ctx context.Context, c ownedDOMTransport, r nativeRequest) (bool, error) {
	read := func(phase string) (ownedClickPlan, error) {
		if err := ctx.Err(); err != nil {
			return ownedClickPlan{}, err
		}
		r.DOMPhase = phase
		s, err := c.eval(ctx, ownedConfirmExpression(r))
		var p ownedClickPlan
		if err != nil {
			return p, err
		}
		if err = json.Unmarshal([]byte(s), &p); err != nil {
			return p, err
		}
		if !normalURLMatches(r.URL, p.URL, r.Family) || p.GroupID != r.GroupID || p.OptionID != r.OptionID {
			return p, fmt.Errorf("配置确认快照的商品或选项归属已变化")
		}
		if !p.Needed {
			return p, nil
		}
		target := "selection-modal-change-btn"
		if r.Action == "ordinary" || r.Action == "custom" {
			target = "scrm-continue"
		} else if r.Action != "select" {
			return p, fmt.Errorf("配置确认操作无效")
		}
		if p.ModalID == "" || p.TargetID != target || p.Point == nil ||
			math.IsNaN(p.Point.X) || math.IsNaN(p.Point.Y) || math.IsInf(p.Point.X, 0) || math.IsInf(p.Point.Y, 0) || p.Point.X < 0 || p.Point.Y < 0 {
			return p, fmt.Errorf("配置确认按钮或位置未确认")
		}
		return p, nil
	}
	plan, err := read("prepare")
	if err != nil || !plan.Needed {
		return false, err
	}
	log.Printf("owned configuration confirmation prepared: action=%s group=%s wanted-id=%s selected-ids=%q modal=%s button=%s label=%q", r.Action, r.GroupID, r.OptionID, plan.SelectedIDs, plan.ModalID, plan.TargetID, plan.Label)
	r.PointX, r.PointY = plan.Point.X, plan.Point.Y
	err = ownedBrowserClick(ctx, c, plan, func() (ownedClickPlan, error) { return read("hit") })
	if err != nil {
		return false, err
	}
	log.Printf("owned configuration confirmation dispatched: action=%s wanted-id=%s modal=%s; selection and current quote must still be verified", r.Action, r.OptionID, plan.ModalID)
	return true, nil
}
