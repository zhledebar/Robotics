package main

import "strings"

func nativeChoiceNode(n *nativeNode) bool {
	return n != nil && (n.Kind == "Button" || n.Kind == "RadioButton" || n.Kind == "CheckBox")
}
func nativeGroupLabel(page nativePage, id string) string {
	g, ok := nativeGroupByID(page, id)
	if !ok {
		return id
	}
	labels := map[string]string{"processor": "处理器", "graphics card": "显卡", "memory": "内存", "storage": "硬盘", "display": "屏幕", "displays": "屏幕"}
	if value := labels[g.Label]; value != "" {
		return value
	}
	return g.Label
}

// Dell's visible collapsed summary is a sibling of the module region. Bind it
// to the smallest ancestor containing exactly this one header, never another
// module's selection or all option labels concatenated together.
func nativeCollapsedSummary(root, header *nativeNode) string {
	bestCount := int(^uint(0) >> 1)
	value := ""
	walkNative(root, func(n *nativeNode) {
		count, headers := 0, 0
		contains := false
		summaries := map[string]string{}
		walkNative(n, func(c *nativeNode) {
			count++
			if strings.HasPrefix(c.ID, "label-module") {
				headers++
				contains = contains || c == header
			}
			for _, class := range strings.Fields(c.Class) {
				if class == "option-title-collapsed" {
					name := normalSpace(nativeText(c))
					key := nativeOptionKey(name)
					if key != "" {
						summaries[key] = name
					}
				}
			}
		})
		if contains && headers == 1 && len(summaries) == 1 && count < bestCount {
			bestCount = count
			for _, name := range summaries {
				value = name
			}
		}
	})
	return value
}
