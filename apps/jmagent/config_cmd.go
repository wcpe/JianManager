package main

import (
	"fmt"
	"os"
	"sort"
)

// runConfig 分发 `jm config <sub>`（FR-517：多 host 管理）。
func runConfig(cfg Config, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("用法: jm config set-url|get-url|list|use|remove <参数>")
	}
	sub := args[0]
	rest := args[1:]

	switch sub {
	case "set-url":
		if len(rest) != 1 {
			return fmt.Errorf("用法: jm config set-url <url>")
		}
		return configSetURL(rest[0])
	case "get-url":
		return configGetURL(cfg)
	case "list":
		return configList(cfg)
	case "use":
		if len(rest) != 1 {
			return fmt.Errorf("用法: jm config use <host>   （先运行 jm config list 查看）")
		}
		return configUse(rest[0])
	case "remove":
		if len(rest) != 1 {
			return fmt.Errorf("用法: jm config remove <host>")
		}
		return configRemove(rest[0])
	default:
		return fmt.Errorf("未知 config 子命令 %q；支持 set-url / get-url / list / use / remove", sub)
	}
}

// configSetURL 添加或更新一个 host，并设为默认。
func configSetURL(rawURL string) error {
	u := normalizeURL(rawURL)
	if u == "" {
		return fmt.Errorf("URL 不能为空")
	}
	key, err := hostKeyOf(u)
	if err != nil {
		return err
	}
	cf, err := loadConfigFile()
	if err != nil {
		return err
	}
	h := cf.Hosts[key]
	h.URL = u
	cf.Hosts[key] = h
	cf.Default = key
	if err := saveConfigFile(cf); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "已设置默认主机：%s\n", u)
	return nil
}

// configGetURL 打印当前生效的基址。
func configGetURL(cfg Config) error {
	fmt.Fprintln(os.Stdout, cfg.BaseURL)
	return nil
}

// configList 列出全部 host。
func configList(cfg Config) error {
	cf, err := loadConfigFile()
	if err != nil {
		return err
	}
	if len(cf.Hosts) == 0 {
		if cfg.Output == "json" {
			return printJSON(map[string]any{"hosts": []any{}, "default": ""})
		}
		fmt.Fprintln(os.Stdout, "尚未配置任何主机。运行：jm config set-url <url>")
		return nil
	}

	keys := make([]string, 0, len(cf.Hosts))
	for k := range cf.Hosts {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	if cfg.Output == "json" {
		rows := make([]map[string]any, 0, len(keys))
		for _, k := range keys {
			h := cf.Hosts[k]
			rows = append(rows, map[string]any{
				"host":        k,
				"url":         h.URL,
				"username":    h.Username,
				"tokenPrefix": h.TokenPrefix,
				"default":     k == cf.Default,
			})
		}
		return printJSON(map[string]any{"hosts": rows, "default": cf.Default})
	}

	for _, k := range keys {
		h := cf.Hosts[k]
		marker := "  "
		if k == cf.Default {
			marker = "* "
		}
		line := fmt.Sprintf("%s%s  %s", marker, k, h.URL)
		if h.Username != "" {
			line += "  (" + h.Username + ")"
		}
		fmt.Fprintln(os.Stdout, line)
	}
	return nil
}

// configUse 切换默认 host。
func configUse(hostOrURL string) error {
	cf, err := loadConfigFile()
	if err != nil {
		return err
	}
	key := hostOrURL
	if _, ok := cf.Hosts[key]; !ok {
		// 允许传完整 URL，归一化后再查。
		if k, err := hostKeyOf(hostOrURL); err == nil {
			key = k
		}
	}
	if _, ok := cf.Hosts[key]; !ok {
		return fmt.Errorf("未找到主机 %q；运行 jm config list 查看已配置主机", hostOrURL)
	}
	cf.Default = key
	if err := saveConfigFile(cf); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "已切换默认主机：%s\n", cf.Hosts[key].URL)
	return nil
}

// configRemove 删除一个 host 及其本地凭证。
func configRemove(hostOrURL string) error {
	cf, err := loadConfigFile()
	if err != nil {
		return err
	}
	key := hostOrURL
	if _, ok := cf.Hosts[key]; !ok {
		if k, err := hostKeyOf(hostOrURL); err == nil {
			key = k
		}
	}
	h, ok := cf.Hosts[key]
	if !ok {
		return fmt.Errorf("未找到主机 %q", hostOrURL)
	}

	// 先清本地凭证，再删配置。
	if err := deleteTokenFromKeyring(key); err != nil {
		fmt.Fprintf(os.Stderr, "提示: 清除钥匙串失败（%s），继续删除配置。\n", explainKeyringError(err))
	}
	delete(cf.Hosts, key)
	if cf.Default == key {
		cf.Default = ""
		for k := range cf.Hosts {
			cf.Default = k
			break
		}
	}
	if err := saveConfigFile(cf); err != nil {
		return err
	}
	fmt.Fprintf(os.Stdout, "已删除主机：%s\n", h.URL)
	return nil
}
