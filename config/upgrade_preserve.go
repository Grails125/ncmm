package config

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/3899/ncmm/internal/atomicfile"
	"gopkg.in/yaml.v3"
)

// 官方配置完成转换后写入本分支版本，下一次启动不再重置用户手动选择。
func needsOfficialSessionSafetyMigration(version string) bool {
	if strings.Contains(version, "-grails.") {
		return false
	}
	core := strings.SplitN(strings.TrimPrefix(strings.TrimSpace(version), "v"), "-", 2)[0]
	parts := strings.Split(core, ".")
	limit := []int{1, 2, 8}
	for i := range limit {
		n := 0
		if i < len(parts) {
			n, _ = strconv.Atoi(parts[i])
		}
		if n != limit[i] {
			return n < limit[i]
		}
	}
	return false
}

func upgradeConfigFile(path string) error {
	lock, err := acquireConfigWriteLock(path)
	if err != nil {
		return err
	}
	defer lock.Close()
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var original yaml.Node
	decoder := yaml.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&original); err != nil {
		return err
	}
	var extra yaml.Node
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("配置必须是单个 YAML 文档")
	}
	if len(original.Content) != 1 || original.Content[0].Kind != yaml.MappingNode {
		return fmt.Errorf("配置必须是非空 YAML 对象")
	}
	var sourceValues any
	if err := original.Decode(&sourceValues); err != nil {
		return err
	}
	var version verStruct
	if err := original.Decode(&version); err != nil {
		return err
	}
	if err := validateSourceVersion(version.Version); err != nil {
		return err
	}
	updated := cloneYAMLNode(&original)
	migrateNode(updated)
	if _, err := migrateMusicPartnerRangeNodes(updated); err != nil {
		return err
	}
	root := updated.Content[0]
	prepareMergeAliases(root)
	if needsOfficialSessionSafetyMigration(version.Version) {
		if fans := effectiveMappingValue(root, "fansgroup"); fans != nil {
			materializeAlias(fans)
			prepareMergeAliases(fans)
			if value := effectiveMappingValue(fans, "webSession"); value != nil {
				disableSessionNode(value)
			}
		}
	}
	if play := effectiveMappingValue(root, "play"); play != nil {
		materializeAlias(play)
		prepareMergeAliases(play)
		if value := effectiveMappingValue(play, "webSession"); value != nil {
			disableSessionNode(value)
		}
	}
	var defaults yaml.Node
	if err := yaml.Unmarshal(defaultConfigByte, &defaults); err != nil {
		return err
	}
	mergeMissingConfigNodes(root, defaults.Content[0])
	versionNode := effectiveMappingValue(root, "version")
	if versionNode.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
		// 块标量的首尾空白由原 chomping 标记决定，更新版本时一并保留。
		versionNode.Value = strings.Replace(versionNode.Value, strings.TrimSpace(versionNode.Value), defaultConfig.Version, 1)
	} else {
		versionNode.Value = defaultConfig.Version
	}
	versionNode.Tag = "!!str"
	preserveChangedAliasValues(&original, updated)
	output, err := renderYAMLChanges(data, &original, updated)
	if err != nil {
		return err
	}
	// 保留未知字段后严格校验，不能为了导入而静默丢弃尚未支持的内容。
	v := newConfigViper()
	if err := v.ReadConfig(bytes.NewReader(output)); err != nil {
		return err
	}
	if _, err := decodeConfig(v); err != nil {
		return err
	}
	if bytes.Equal(data, output) {
		return nil
	}
	if _, err := BackupConfigFile(path, 5); err != nil {
		return err
	}
	return atomicfile.Write(path, output, 0644)
}

func disableSessionNode(value *yaml.Node) {
	value.Kind, value.Value, value.Tag, value.Alias = yaml.ScalarNode, "false", "!!bool", nil
}

func cloneYAMLNode(root *yaml.Node) *yaml.Node {
	seen := make(map[*yaml.Node]*yaml.Node)
	var clone func(*yaml.Node) *yaml.Node
	clone = func(node *yaml.Node) *yaml.Node {
		if node == nil {
			return nil
		}
		if copy := seen[node]; copy != nil {
			return copy
		}
		copy := *node
		seen[node] = &copy
		copy.Content = make([]*yaml.Node, len(node.Content))
		for i, child := range node.Content {
			copy.Content[i] = clone(child)
		}
		// 别名保留原值快照；被引用节点改变时再局部展开，避免连带覆盖用户值。
		copy.Alias = node.Alias
		return &copy
	}
	return clone(root)
}

func materializeAlias(node *yaml.Node) {
	if node.Kind != yaml.AliasNode {
		return
	}
	var resolved func(*yaml.Node) *yaml.Node
	resolved = func(source *yaml.Node) *yaml.Node {
		if source.Kind == yaml.AliasNode {
			return resolved(source.Alias)
		}
		copy := *source
		copy.Anchor, copy.Alias = "", nil
		copy.Content = make([]*yaml.Node, len(source.Content))
		for i, child := range source.Content {
			copy.Content[i] = resolved(child)
		}
		return &copy
	}
	copy := resolved(node)
	copy.Line, copy.Column = node.Line, node.Column
	copy.HeadComment, copy.LineComment, copy.FootComment = node.HeadComment, node.LineComment, node.FootComment
	*node = *copy
}

func preserveChangedAliasValues(before, after *yaml.Node) {
	pairs := make(map[*yaml.Node]*yaml.Node)
	var pair func(*yaml.Node, *yaml.Node)
	pair = func(old, next *yaml.Node) {
		pairs[old] = next
		for i, child := range old.Content {
			if i < len(next.Content) {
				pair(child, next.Content[i])
			}
		}
	}
	pair(before, after)
	var visit func(*yaml.Node)
	visit = func(node *yaml.Node) {
		if node.Kind == yaml.AliasNode {
			if target := pairs[node.Alias]; target != nil && !yamlSemanticEqual(node.Alias, target) {
				materializeAlias(node)
			}
			return
		}
		for _, child := range node.Content {
			visit(child)
		}
	}
	visit(after)
}

// 合并键中的别名在本位置展开，后续补字段不会修改其他位置的共享映射。
func prepareMergeAliases(node *yaml.Node) {
	if node.Kind != yaml.MappingNode {
		return
	}
	for i := 0; i < len(node.Content); i += 2 {
		if node.Content[i].Value != "<<" {
			continue
		}
		value := node.Content[i+1]
		materializeAlias(value)
		if value.Kind == yaml.SequenceNode {
			for _, item := range value.Content {
				materializeAlias(item)
			}
		}
	}
}

func effectiveMappingValue(mapping *yaml.Node, key string) *yaml.Node {
	seen := make(map[*yaml.Node]bool)
	var find func(*yaml.Node) *yaml.Node
	find = func(node *yaml.Node) *yaml.Node {
		if node.Kind == yaml.AliasNode {
			node = node.Alias
		}
		if node.Kind != yaml.MappingNode || seen[node] {
			return nil
		}
		seen[node] = true
		if direct := mappingNodeValue(node, key); direct != nil {
			return direct
		}
		for i := 0; i < len(node.Content); i += 2 {
			if node.Content[i].Value != "<<" {
				continue
			}
			merge := node.Content[i+1]
			if merge.Kind == yaml.SequenceNode {
				for _, item := range merge.Content {
					if value := find(item); value != nil {
						return value
					}
				}
			} else if value := find(merge); value != nil {
				return value
			}
		}
		return nil
	}
	return find(mapping)
}

// 在用户节点上补缺失字段，不把模板中的占位 Token 或乐评合入动态映射。
func mergeMissingConfigNodes(user, defaults *yaml.Node) {
	materializeAlias(user)
	if user.Kind != yaml.MappingNode || defaults.Kind != yaml.MappingNode {
		return
	}
	prepareMergeAliases(user)
	for i := 0; i < len(defaults.Content); i += 2 {
		key, value := defaults.Content[i], defaults.Content[i+1]
		existing := effectiveMappingValue(user, key.Value)
		if existing == nil {
			keyCopy, valueCopy := cloneYAMLNode(key), cloneYAMLNode(value)
			clearYAMLPositions(keyCopy)
			clearYAMLPositions(valueCopy)
			user.Content = append(user.Content, keyCopy, valueCopy)
			continue
		}
		if key.Value != "antiCheatTokens" && key.Value != "commentCheckTokens" && key.Value != "comments" {
			mergeMissingConfigNodes(existing, value)
		}
	}
}

func clearYAMLPositions(node *yaml.Node) {
	node.Line, node.Column = 0, 0
	for _, child := range node.Content {
		clearYAMLPositions(child)
	}
}

type yamlSourceEdit struct {
	start, end int
	text       string
}

// 只改发生迁移的 token 并插入缺失字段；原有注释、空行和标量风格不经重新编码。
func renderYAMLChanges(data []byte, before, after *yaml.Node) ([]byte, error) {
	lines := []int{0}
	for i, b := range data {
		if b == '\n' {
			lines = append(lines, i+1)
		}
	}
	newline := "\n"
	if bytes.Contains(data, []byte("\r\n")) {
		newline = "\r\n"
	}
	offset := func(node *yaml.Node) int {
		index := lines[node.Line-1]
		if node.Line == 1 && bytes.HasPrefix(data, []byte("\xef\xbb\xbf")) {
			index += 3
		}
		for column := 1; column < node.Column && index < len(data); column++ {
			_, size := utf8.DecodeRune(data[index:])
			index += size
		}
		return index
	}
	var edits []yamlSourceEdit
	insertions := make(map[int][]string)
	var visit func(*yaml.Node, *yaml.Node, int) error
	visit = func(old, next *yaml.Node, limit int) error {
		if old.Kind == yaml.DocumentNode {
			return visit(old.Content[0], next.Content[0], limit)
		}
		if old.Kind == yaml.MappingNode && old.Style&yaml.FlowStyle == 0 && next.Kind == yaml.MappingNode {
			for i := 0; i < len(old.Content); i += 2 {
				if err := visit(old.Content[i], next.Content[i], limit); err != nil {
					return err
				}
				childLimit := limit
				if i+2 < len(old.Content) {
					childLimit = yamlHeadingStart(data, lines, old.Content[i+2])
				}
				if err := visit(old.Content[i+1], next.Content[i+1], childLimit); err != nil {
					return err
				}
			}
			if len(next.Content) > len(old.Content) {
				fragment := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map", Content: next.Content[len(old.Content):]}
				encoded, err := encodeYAMLFragment(fragment)
				if err != nil {
					return err
				}
				indent := old.Column - 1
				if len(old.Content) > 0 {
					indent = old.Content[0].Column - 1
				}
				prefix := strings.Repeat(" ", indent)
				parts := strings.Split(strings.TrimSuffix(encoded, "\n"), "\n")
				for i := range parts {
					if parts[i] != "" {
						parts[i] = prefix + parts[i]
					}
				}
				insertions[limit] = append(insertions[limit], strings.Join(parts, newline)+newline)
			}
			return nil
		}
		if yamlSemanticEqual(old, next) {
			return nil
		}
		if old.Kind == yaml.SequenceNode && old.Style&yaml.FlowStyle == 0 && next.Kind == yaml.SequenceNode && len(old.Content) == len(next.Content) {
			for i := range old.Content {
				if err := visit(old.Content[i], next.Content[i], limit); err != nil {
					return err
				}
			}
			return nil
		}
		if old.Kind != yaml.ScalarNode && old.Kind != yaml.AliasNode && old.Style&yaml.FlowStyle == 0 {
			return fmt.Errorf("无法安全保留该 YAML 结构，请手动调整配置")
		}
		start := offset(old)
		if old.Kind == yaml.ScalarNode && old.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
			edit, err := yamlBlockScalarEdit(data, start, limit, old, next)
			if err != nil {
				return err
			}
			edits = append(edits, edit)
			return nil
		}
		end, err := yamlTokenEnd(data, start)
		if err != nil {
			return err
		}
		copy := *next
		copy.HeadComment, copy.LineComment, copy.FootComment = "", "", ""
		encoded, err := encodeYAMLFragment(&copy)
		if err != nil {
			return err
		}
		encoded = strings.TrimSuffix(encoded, "\n")
		edits = append(edits, yamlSourceEdit{start, end, strings.ReplaceAll(encoded, "\n", newline)})
		return nil
	}
	limit := len(data)
	for i := len(lines) - 1; i >= 0; i-- {
		line := strings.TrimSpace(string(data[lines[i]:]))
		if line == "..." {
			limit = lines[i]
			break
		}
		if line != "" {
			break
		}
	}
	if err := visit(before, after, limit); err != nil {
		return nil, err
	}
	for position, parts := range insertions {
		text := strings.Join(parts, "")
		if position > 0 && data[position-1] != '\n' {
			text = newline + text
		}
		edits = append(edits, yamlSourceEdit{position, position, text})
	}
	sort.SliceStable(edits, func(i, j int) bool { return edits[i].start > edits[j].start })
	result := append([]byte(nil), data...)
	last := len(data) + 1
	for _, edit := range edits {
		if edit.end > last || edit.start > edit.end {
			return nil, fmt.Errorf("配置迁移编辑范围冲突，已保留原文件")
		}
		result = append(append(append([]byte(nil), result[:edit.start]...), []byte(edit.text)...), result[edit.end:]...)
		last = edit.start
	}
	var rendered yaml.Node
	if err := yaml.Unmarshal(result, &rendered); err != nil {
		return nil, err
	}
	if !yamlSemanticEqual(after, &rendered) {
		return nil, fmt.Errorf("配置迁移结果不一致，已保留原文件")
	}
	return result, nil
}

// 单行内容的块标量只替换正文，保留 header、chomping、缩进、空行和换行风格。
func yamlBlockScalarEdit(data []byte, start, limit int, before, after *yaml.Node) (yamlSourceEdit, error) {
	oldValue, newValue := strings.TrimSpace(before.Value), strings.TrimSpace(after.Value)
	if after.Kind != yaml.ScalarNode || before.Style != after.Style || oldValue == "" || strings.ContainsAny(oldValue+newValue, "\r\n") || strings.Replace(before.Value, oldValue, newValue, 1) != after.Value {
		return yamlSourceEdit{}, fmt.Errorf("无法安全保留该 YAML 块标量，请手动调整配置")
	}
	headerStart := bytes.LastIndexByte(data[:start], '\n') + 1
	headerIndent := 0
	for headerStart+headerIndent < start && data[headerStart+headerIndent] == ' ' {
		headerIndent++
	}
	headerEnd := bytes.IndexByte(data[start:limit], '\n')
	if headerEnd < 0 {
		return yamlSourceEdit{}, fmt.Errorf("无法识别 YAML 块标量正文")
	}
	bodyStart, bodyEnd := start+headerEnd+1, limit
	for position := bodyStart; position < limit; {
		lineEnd := limit
		if index := bytes.IndexByte(data[position:limit], '\n'); index >= 0 {
			lineEnd = position + index + 1
		}
		line := data[position:lineEnd]
		indent := len(line) - len(bytes.TrimLeft(line, " "))
		if len(bytes.TrimSpace(line)) > 0 && indent <= headerIndent {
			bodyEnd = position
			break
		}
		position = lineEnd
	}
	body := data[bodyStart:bodyEnd]
	if bytes.Count(body, []byte(oldValue)) != 1 {
		return yamlSourceEdit{}, fmt.Errorf("无法安全识别 YAML 块标量内容")
	}
	valueStart := bodyStart + bytes.Index(body, []byte(oldValue))
	return yamlSourceEdit{valueStart, valueStart + len(oldValue), newValue}, nil
}

func encodeYAMLFragment(node *yaml.Node) (string, error) {
	var output bytes.Buffer
	encoder := yaml.NewEncoder(&output)
	encoder.SetIndent(2)
	if err := encoder.Encode(node); err != nil {
		return "", err
	}
	if err := encoder.Close(); err != nil {
		return "", err
	}
	return output.String(), nil
}

func yamlSemanticEqual(a, b *yaml.Node) bool {
	var first, second any
	if a.Decode(&first) != nil || b.Decode(&second) != nil {
		return false
	}
	return reflect.DeepEqual(first, second)
}

func yamlHeadingStart(data []byte, lines []int, key *yaml.Node) int {
	index := key.Line - 1
	if key.HeadComment == "" {
		return lines[index]
	}
	for index > 0 {
		line := strings.TrimRight(string(data[lines[index-1]:lines[index]]), "\r\n")
		trimmed := strings.TrimLeft(line, " ")
		if strings.TrimSpace(line) == "" || (strings.HasPrefix(trimmed, "#") && len(line)-len(trimmed) <= key.Column-1) {
			index--
			continue
		}
		break
	}
	return lines[index]
}

// 迁移只改标量或流式集合，扫描时尊重引号与转义，避免碰到值内的 # 和括号。
func yamlTokenEnd(data []byte, start int) (int, error) {
	var quote byte
	depth := 0
	for i := start; i < len(data); i++ {
		c := data[i]
		if quote != 0 {
			if quote == '"' && c == '\\' {
				i++
				continue
			}
			if c == quote {
				if quote == '\'' && i+1 < len(data) && data[i+1] == '\'' {
					i++
					continue
				}
				quote = 0
				if depth == 0 {
					return i + 1, nil
				}
			}
			continue
		}
		switch c {
		case '\'', '"':
			quote = c
		case '{', '[':
			depth++
		case '}', ']':
			depth--
			if depth == 0 {
				return i + 1, nil
			}
		case '#':
			if depth == 0 && (i == start || data[i-1] == ' ' || data[i-1] == '\t') {
				return trimYAMLTokenEnd(data, start, i), nil
			}
		case ':':
			if depth == 0 && i+1 < len(data) && (data[i+1] == ' ' || data[i+1] == '\n' || data[i+1] == '\r') {
				return trimYAMLTokenEnd(data, start, i), nil
			}
		case '\r', '\n':
			if depth == 0 {
				return trimYAMLTokenEnd(data, start, i), nil
			}
		}
	}
	if quote != 0 || depth != 0 {
		return 0, fmt.Errorf("无法识别配置标量的结束位置")
	}
	return trimYAMLTokenEnd(data, start, len(data)), nil
}

func trimYAMLTokenEnd(data []byte, start, end int) int {
	for end > start && (data[end-1] == ' ' || data[end-1] == '\t') {
		end--
	}
	return end
}
