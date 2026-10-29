// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

package formcheck

import "testing"

// TestDriverFormsAreLocalized 仓库里每个 form-create 表单都必须：
//   - 显示文案是 {{$t.key}}，而不是中文字面量；
//   - 每个 key 在 zh-cn 与 en 里都有译文，英文不含中文；
//   - 没有未被引用的多余 key；
//   - 协议表单不含 ASCII 分号（会被迁移器切开），任何表单不含 ASCII 单引号。
func TestDriverFormsAreLocalized(t *testing.T) {
	forms, err := Extract("../..")
	if err != nil {
		t.Fatalf("提取表单失败: %v", err)
	}
	if len(forms) < 20 {
		t.Fatalf("只提取到 %d 个表单，提取逻辑可能失效", len(forms))
	}
	for _, form := range forms {
		for _, problem := range Check(form) {
			t.Error(problem)
		}
	}
}
