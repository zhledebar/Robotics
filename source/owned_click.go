package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"time"
)

type ownedDOMTransport interface {
	eval(context.Context, string) (string, error)
	call(context.Context, string, any) (json.RawMessage, error)
}

type ownedClickPlan struct {
	URL         string                  `json:"url"`
	GroupID     string                  `json:"groupID"`
	OptionID    string                  `json:"optionID"`
	TargetID    string                  `json:"targetID"`
	SelectedIDs []string                `json:"selectedIDs"`
	Already     bool                    `json:"already"`
	Point       *struct{ X, Y float64 } `json:"point"`
	Needed      bool                    `json:"needed"`
	ModalID     string                  `json:"modalID"`
	Label       string                  `json:"label"`
}

func ownedDOMOperation(ctx context.Context, c ownedDOMTransport, r nativeRequest) error {
	if r.Action != "select" && r.Action != "expand" && r.Action != "collapse" {
		return fmt.Errorf("未知核心配置操作")
	}
	readPlan := func(phase string) (ownedClickPlan, error) {
		if err := ctx.Err(); err != nil {
			return ownedClickPlan{}, err
		}
		r.DOMPhase = phase
		s, err := c.eval(ctx, ownedDOMExpression(r, r.Action))
		var plan ownedClickPlan
		if err != nil {
			return plan, err
		}
		if err = json.Unmarshal([]byte(s), &plan); err != nil {
			return plan, err
		}
		targetID := "label-" + r.GroupID
		if r.Action == "select" {
			if r.OptionID == "" {
				return plan, fmt.Errorf("核心选项ID缺失；没有猜测点击目标")
			}
			targetID = r.OptionID
		}
		if !normalURLMatches(r.URL, plan.URL, r.Family) || plan.GroupID != r.GroupID || plan.OptionID != r.OptionID || plan.TargetID != targetID {
			return plan, fmt.Errorf("核心点击目标归属或选项ID已变化")
		}
		if r.Action == "select" && plan.Already && (len(plan.SelectedIDs) != 1 || plan.SelectedIDs[0] != r.OptionID) {
			return plan, fmt.Errorf("核心选项选中状态不唯一或ID不对应")
		}
		if !plan.Already && (plan.Point == nil || math.IsNaN(plan.Point.X) || math.IsNaN(plan.Point.Y) || math.IsInf(plan.Point.X, 0) || math.IsInf(plan.Point.Y, 0) || plan.Point.X < 0 || plan.Point.Y < 0) {
			return plan, fmt.Errorf("核心点击位置未确认")
		}
		return plan, nil
	}
	plan, err := readPlan("prepare")
	if err != nil {
		return err
	}
	log.Printf("owned core click prepared: action=%s group=%s wanted-id=%s wanted-name=%q selected-ids=%q already=%v point=%+v", r.Action, r.GroupID, r.OptionID, r.OptionName, plan.SelectedIDs, plan.Already, plan.Point)
	if plan.Already {
		return nil
	}
	r.PointX, r.PointY = plan.Point.X, plan.Point.Y
	if err = ownedBrowserClick(ctx, c, plan, func() (ownedClickPlan, error) { return readPlan("hit") }); err != nil {
		return err
	}
	log.Printf("owned core browser click dispatched: action=%s group=%s wanted-id=%s; actual selection still requires snapshot verification", r.Action, r.GroupID, r.OptionID)
	return nil
}

func ownedBrowserClick(ctx context.Context, c ownedDOMTransport, plan ownedClickPlan, recheck func() (ownedClickPlan, error)) error {
	x, y := plan.Point.X, plan.Point.Y
	if _, err := c.call(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseMoved", "x": x, "y": y, "button": "none", "buttons": 0, "pointerType": "mouse"}); err != nil {
		return err
	}
	// Hover can move/cover an option. Recheck the same element under the exact
	// point immediately before pressing, without clicking JavaScript handlers.
	checked, err := recheck()
	if err != nil {
		return err
	}
	if checked.Already {
		return nil
	}
	if checked.Point == nil || checked.URL != plan.URL || checked.TargetID != plan.TargetID || checked.ModalID != plan.ModalID || checked.Point.X != x || checked.Point.Y != y {
		return fmt.Errorf("核心点击位置或商品页已变化")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, pressErr := c.call(ctx, "Input.dispatchMouseEvent", map[string]any{"type": "mousePressed", "x": x, "y": y, "button": "left", "buttons": 1, "clickCount": 1, "pointerType": "mouse"})
	// A response error may occur after the press was delivered. Release only in
	// this same owned page, including cancellation, so the button is not held.
	releaseCtx := ctx
	if ctx.Err() != nil || pressErr != nil {
		var cancel context.CancelFunc
		releaseCtx, cancel = context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
	}
	_, releaseErr := c.call(releaseCtx, "Input.dispatchMouseEvent", map[string]any{"type": "mouseReleased", "x": x, "y": y, "button": "left", "buttons": 0, "clickCount": 1, "pointerType": "mouse"})
	if pressErr != nil {
		return pressErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if releaseErr != nil {
		return releaseErr
	}
	return nil
}
