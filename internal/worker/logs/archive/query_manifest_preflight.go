package archive

import (
	"encoding/json"
	"fmt"
	"strings"
)

const (
	archiveMaxEvents  uint64 = 100000
	archiveMaxObjects int    = 1024
)

func preflightManifestJSON(decoder *json.Decoder, field string, depth int) error {
	if depth > 64 {
		return fmt.Errorf("archive: manifest JSON nesting exceeds bound")
	}
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, ok := token.(json.Delim)
	if !ok {
		return nil
	}
	switch delimiter {
	case '{':
		count := 0
		var seenContainers map[string]struct{}
		for decoder.More() {
			name, err := decoder.Token()
			if err != nil {
				return err
			}
			count++
			limit := int(archiveMaxEvents)
			if field == "objects" {
				limit = archiveMaxObjects
			}
			if count > limit {
				return fmt.Errorf("archive: manifest JSON object cardinality exceeds bound")
			}
			child := ""
			key, ok := name.(string)
			if !ok {
				return fmt.Errorf("archive: invalid manifest JSON field")
			}
			// 与encoding/json的字段折叠一致，不能用大写或Unicode折叠绕过强类型容器上界。
			switch field {
			case "root":
				switch {
				case strings.EqualFold(key, "raw_files"):
					child = "raw_files"
				case strings.EqualFold(key, "coverage"):
					child = "coverage"
				case strings.EqualFold(key, "checksums"):
					child = "checksums"
				}
			case "coverage":
				switch {
				case strings.EqualFold(key, "source_generations"):
					child = "source_generations"
				case strings.EqualFold(key, "partial_reasons"):
					child = "partial_reasons"
				}
			case "checksums":
				if strings.EqualFold(key, "objects") {
					child = "objects"
				}
			}
			if child != "" {
				// 强类型结构体的重复规范字段会合并到同一个map，不能按每次出现重置数量闸。
				// 这里只处理已识别的容器字段，checksums.objects内的真实路径仍严格区分大小写。
				if _, exists := seenContainers[child]; exists {
					return fmt.Errorf("archive: duplicate normalized manifest container %s", child)
				}
				if seenContainers == nil {
					seenContainers = make(map[string]struct{}, 3)
				}
				seenContainers[child] = struct{}{}
			}
			if err := preflightManifestJSON(decoder, child, depth+1); err != nil {
				return err
			}
		}
	case '[':
		limit := int(archiveMaxEvents)
		if field == "raw_files" || field == "partial_reasons" {
			limit = archiveMaxObjects
		}
		count := 0
		for decoder.More() {
			count++
			if count > limit {
				return fmt.Errorf("archive: manifest JSON array cardinality exceeds bound")
			}
			if err := preflightManifestJSON(decoder, "", depth+1); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("archive: invalid manifest JSON delimiter")
	}
	_, err = decoder.Token()
	return err
}
