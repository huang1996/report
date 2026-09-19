package main

// webdav.go —— WebDAV 上传（保留自 safeline-report）：MKCOL 建目录 + PUT 上传

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
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

// UploadReport 按 safeline-report 原逻辑组织远端路径：report/<审核人>/<yyyymmdd>/<文件名>
// 审核人取 REPORT_ENGINEER（env）或 -report_engineer（参数）；合并自原 REPORT_ONWER，未配置时上传到 report/default/
func UploadReport(cfg *Config, localPath string) error {
	owner := strings.TrimSpace(cfg.Engineer)
	if owner == "" {
		log.Warnf("REPORT_ENGINEER 未配置，报告将上传到 report/default/ 目录")
		owner = "default"
	}
	remoteDir := "report/" + owner + "/" + time.Now().Format("20060102")
	remotePath := remoteDir + "/" + filepath.Base(localPath)
	if err := WebdavUpload(cfg.WebdavHost, cfg.WebdavLogin, cfg.WebdavPass, localPath, remotePath); err != nil {
		return err
	}
	log.Infof("已上传 WebDAV：%s", remotePath)
	return nil
}
