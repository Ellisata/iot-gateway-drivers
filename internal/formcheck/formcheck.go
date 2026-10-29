// SPDX-License-Identifier: AGPL-3.0-only
// Copyright (c) 2026 Edwin and iot-gateway contributors

// Package formcheck 校验驱动自带的 form-create 表单是否满足多语言约束。
//
// 表单文案是运行期数据：显示字段必须是 `{{$t.key}}`，译文必须同时存在于
// options.language 的 zh-cn 与 en 里。漏一条的症状是英文界面显示空标签或裸 key，
// 而 go build/test 不会替我们发现 —— 所以用一条仓库级测试把它钉住。
package formcheck

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"
)

// Form 从 Go 源码里提取出的一个表单字面量。
type Form struct {
	File  string
	Const string
	Raw   string
}

var formRe = regexp.MustCompile("(?m)^[ \\t]*(?:const\\s+)?(\\w+)\\s*=\\s*`(\\{[\\s\\S]*?\"rule\"[\\s\\S]*?\\})`")

var displayKeys = map[string]bool{
	"title":       true,
	"info":        true,
	"label":       true,
	"placeholder": true,
}

var tokenRe = regexp.MustCompile(`^\{\{\s*\$t\.([\w.$-]+)\s*\}\}$`)

// Extract 遍历仓库根目录，提取 main.go / probe.go / pointscan.go 中的表单。
func Extract(root string) ([]Form, error) {
	var forms []Form
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			name := d.Name()
			if name == ".git" || name == "internal" || strings.HasPrefix(name, "plugins-") {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if name != "main.go" && name != "probe.go" && name != "pointscan.go" {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		for _, m := range formRe.FindAllStringSubmatch(string(raw), -1) {
			forms = append(forms, Form{File: filepath.ToSlash(rel), Const: m[1], Raw: m[2]})
		}
		return nil
	})
	return forms, err
}

// Check 返回该表单不满足约束的问题列表。
func Check(form Form) []string {
	var problems []string
	add := func(format string, args ...any) {
		problems = append(problems, form.File+"#"+form.Const+": "+fmt.Sprintf(format, args...))
	}

	var root map[string]any
	if err := json.Unmarshal([]byte(form.Raw), &root); err != nil {
		return []string{form.File + "#" + form.Const + ": 不是合法 JSON: " + err.Error()}
	}
	if strings.Contains(form.Raw, "'") {
		add("表单里出现 ASCII 单引号（迁移 SQL 用单引号字面量）")
	}
	if strings.HasSuffix(form.File, "main.go") && strings.Contains(form.Raw, ";") {
		add("协议表单里出现 ASCII 分号（迁移器按分号切分）")
	}

	language := map[string]map[string]string{}
	if options, ok := root["options"].(map[string]any); ok {
		if rawLang, ok := options["language"].(map[string]any); ok {
			for _, loc := range []string{"zh-cn", "en"} {
				dict := map[string]string{}
				if m, ok := rawLang[loc].(map[string]any); ok {
					for k, v := range m {
						if s, ok := v.(string); ok {
							dict[k] = s
						}
					}
				}
				language[loc] = dict
			}
		}
	}

	used := map[string]bool{}
	var walk func(v any, inDisplay bool)
	walk = func(v any, inDisplay bool) {
		switch x := v.(type) {
		case map[string]any:
			for k, child := range x {
				walk(child, displayKeys[k] || inDisplay && (k == "children"))
			}
		case []any:
			for _, child := range x {
				walk(child, inDisplay)
			}
		case string:
			if m := tokenRe.FindStringSubmatch(x); m != nil {
				if !inDisplay {
					add("多语言 token 出现在非显示字段: %q", x)
					return
				}
				used[m[1]] = true
				zh, en := language["zh-cn"][m[1]], language["en"][m[1]]
				if zh == "" {
					add("token %q 缺少 zh-cn 译文", m[1])
				}
				if en == "" {
					add("token %q 缺少 en 译文", m[1])
				} else if containsCJK(en) {
					add("token %q 的英文仍含中文: %q", m[1], en)
				}
				return
			}
			if inDisplay && containsCJK(x) {
				add("显示字段仍有中文字面量: %q", x)
			}
		}
	}
	if rule, ok := root["rule"].([]any); ok {
		for _, r := range rule {
			walkRule(r, walk)
		}
	}

	for _, loc := range []string{"zh-cn", "en"} {
		for k := range language[loc] {
			if !used[k] {
				add("%s 里有多余的 key %q（没有任何 token 引用）", loc, k)
			}
		}
	}
	return problems
}

// walkRule 以 rule 为边界遍历。inDisplay 只对 title/info/props.placeholder/options[].label 生效，
// 其余字段（value、type、field…）一旦出现 token 就按"非显示字段"报错。
func walkRule(v any, walk func(any, bool)) {
	rule, ok := v.(map[string]any)
	if !ok {
		return
	}
	for k, child := range rule {
		switch k {
		case "title", "info":
			walk(child, true)
		case "props":
			if props, ok := child.(map[string]any); ok {
				for pk, pv := range props {
					walk(pv, pk == "placeholder")
				}
			} else {
				walk(child, false)
			}
		case "options":
			if opts, ok := child.([]any); ok {
				for _, o := range opts {
					if om, ok := o.(map[string]any); ok {
						for ok2, ov := range om {
							walk(ov, ok2 == "label")
						}
					}
				}
			}
		case "children":
			if kids, ok := child.([]any); ok {
				for _, kid := range kids {
					walkRule(kid, walk)
				}
			}
		default:
			walk(child, false)
		}
	}
}

func containsCJK(s string) bool {
	for _, r := range s {
		if unicode.In(r, unicode.Han) {
			return true
		}
	}
	return false
}
