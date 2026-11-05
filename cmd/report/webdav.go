package main

// webdav.go —— WebDAV 上传（保留自 safeline-report）：MKCOL 建目录 + PUT 上传

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var davClient = &http.Client{Timeout: 120 * time.Second}

// WebdavUpload 把本地文件上传到 host + remotePath（如 https://dav.example.com/dav + report/owner/20260913/x.docx）
func WebdavUpload(host, user, pass, localPath, remotePath string) error {
	if host == "" {
		return fmt.Errorf("WEBDAV_HOSTNAME 未配置")
	}
	if !strings.HasPrefix(host, "http://") && !strings.HasPrefix(host, "https://") {
		host = "https://" + host
	}
	base := strings.TrimRight(host, "/")

	// 逐级建目录（已存在返回 405/301 均视为成功）
	parts := strings.Split(strings.Trim(remotePath, "/"), "/")
	dir := base
	for _, p := range parts[:len(parts)-1] {
		dir += "/" + p
		req, err := http.NewRequest("MKCOL", dir, nil)
		if err != nil {
			return err
		}
		req.SetBasicAuth(user, pass)
		resp, err := davClient.Do(req)
		if err != nil {
			return fmt.Errorf("WebDAV MKCOL %s 失败: %v", dir, err)
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode >= 400 && resp.StatusCode != 405 && resp.StatusCode != 301 {
			return fmt.Errorf("WebDAV MKCOL %s 返回 %d", dir, resp.StatusCode)
		}
	}

	f, err := os.Open(localPath)
	if err != nil {
		return err
	}
	defer f.Close()

	remote := base + "/" + strings.Trim(remotePath, "/")
	req, err := http.NewRequest("PUT", remote, f)
	if err != nil {
		return err
	}
	req.SetBasicAuth(user, pass)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := davClient.Do(req)
	if err != nil {
		return fmt.Errorf("WebDAV PUT %s 失败: %v", remote, err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("WebDAV PUT %s 返回 %d", remote, resp.StatusCode)
	}
	return nil
}

// UploadReport 组织远端目录并上传。
//
// 配了 WEBDAV_DIR 时按模板解析（变量见 renderUploadDir），例如
//
//	WEBDAV_DIR=/{REPORT_ENGINEER}/泸州市司法局/{year}/{month}/
//
// 解析为 /张三/泸州市司法局/2026/11，工程师为空则解析为 /泸州市司法局/2026/11；
// 未配置时沿用 safeline-report 原逻辑：report/<审核人>/<yyyymmdd>/，审核人为空则用 default。
//
// 目录存在与否由 WebdavUpload 逐级 MKCOL 保证：已存在时服务端返回 405/301，视为成功。
func UploadReport(cfg *Config, localPath string) error {
	var remoteDir string
	if tmpl := strings.TrimSpace(cfg.WebdavDir); tmpl != "" {
		dir, unknown := renderUploadDir(tmpl, time.Now(), cfg.Engineer)
		if len(unknown) > 0 {
			log.Warnf("WEBDAV_DIR 中有未知变量 %s，已按字面保留，请检查模板", strings.Join(unknown, "、"))
		}
		remoteDir = dir
		log.Infof("WebDAV 上传目录（按模板解析）：%s", remoteDir)
	} else {
		owner := strings.TrimSpace(cfg.Engineer)
		if owner == "" {
			log.Warnf("REPORT_ENGINEER 未配置，报告将上传到 report/default/ 目录")
			owner = "default"
		}
		remoteDir = "report/" + owner + "/" + time.Now().Format("20060102")
	}
	remotePath := strings.TrimRight(remoteDir, "/") + "/" + filepath.Base(localPath)
	if err := WebdavUpload(cfg.WebdavHost, cfg.WebdavLogin, cfg.WebdavPass, localPath, remotePath); err != nil {
		return err
	}
	log.Infof("已上传 WebDAV：%s", remotePath)
	return nil
}

// uploadDirVarRe 匹配上传目录模板里的 {变量名}
var uploadDirVarRe = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// renderUploadDir 按模板解析上传目录，返回归一化后的绝对路径（前导 /、无尾随 /）与未知变量列表。
//
// 支持变量（大小写不敏感）：
//   - {REPORT_ENGINEER}：同 -report_engineer，未配置时替换为空串
//   - {datetime}：当前日期 yyyymmdd，如 20261105
//   - {year}：年 yyyy，如 2026
//   - {month}：月 mm（补零），如 03 / 11
//   - {day}：日 dd（补零），如 03
//
// 归一化时会丢掉空段，所以变量为空不会留下多余的 //：`/{REPORT_ENGINEER}/某局/{year}` 在工程师为空时
// 解析为 `/某局/2026`。未知变量按字面保留并回传给调用方 WARN，避免拼错却静默丢路径。
func renderUploadDir(tmpl string, now time.Time, engineer string) (string, []string) {
	vars := map[string]string{
		"REPORT_ENGINEER": strings.TrimSpace(engineer),
		"DATETIME":        now.Format("20060102"),
		"YEAR":            now.Format("2006"),
		"MONTH":           now.Format("01"),
		"DAY":             now.Format("02"),
	}
	var unknown []string
	s := uploadDirVarRe.ReplaceAllStringFunc(tmpl, func(m string) string {
		key := strings.ToUpper(m[1 : len(m)-1])
		if v, ok := vars[key]; ok {
			return v
		}
		unknown = append(unknown, m)
		return m
	})
	parts := make([]string, 0, 8)
	for _, p := range strings.Split(s, "/") {
		if p = strings.TrimSpace(p); p != "" {
			parts = append(parts, p)
		}
	}
	return "/" + strings.Join(parts, "/"), unknown
}
