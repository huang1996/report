package main

// webdav_test.go —— 上传目录模板解析与实际上传路径

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRenderUploadDir(t *testing.T) {
	mar := time.Date(2026, 3, 7, 15, 4, 5, 0, time.Local)
	nov := time.Date(2026, 11, 5, 9, 30, 0, 0, time.Local)

	cases := []struct {
		name     string
		tmpl     string
		now      time.Time
		engineer string
		want     string
		unknown  []string
	}{
		{"工程师有值", "/{REPORT_ENGINEER}/泸州市司法局/{year}/{month}/", nov, "张三", "/张三/泸州市司法局/2026/11", nil},
		{"工程师为空", "/{REPORT_ENGINEER}/泸州市司法局/{year}/{month}/", nov, "", "/泸州市司法局/2026/11", nil},
		{"工程师只有空格", "/{REPORT_ENGINEER}/某局/{year}", nov, "   ", "/某局/2026", nil},
		{"日期时间", "/{datetime}/报告", nov, "", "/20261105/报告", nil},
		{"月日补零", "/{year}/{month}/{day}", mar, "", "/2026/03/07", nil},
		{"变量大小写不敏感", "/{Year}/{Month}/{Day}", mar, "", "/2026/03/07", nil},
		{"无前导斜杠", "泸州市司法局/{year}", nov, "", "/泸州市司法局/2026", nil},
		{"重复斜杠与空格", "//泸州市司法局///{year}// ", nov, "", "/泸州市司法局/2026", nil},
		{"未知变量保留", "/{foo}/{year}", nov, "", "/{foo}/2026", []string{"{foo}"}},
		{"纯变量且为空", "/{REPORT_ENGINEER}", nov, "", "/", nil},
	}
	for _, c := range cases {
		got, unknown := renderUploadDir(c.tmpl, c.now, c.engineer)
		if got != c.want {
			t.Errorf("%s：目录 = %q，期望 %q", c.name, got, c.want)
		}
		if strings.Join(unknown, ",") != strings.Join(c.unknown, ",") {
			t.Errorf("%s：未知变量 = %v，期望 %v", c.name, unknown, c.unknown)
		}
	}
}

// davStub 记录 MKCOL / PUT 的请求路径，按 201 返回
type davStub struct {
	mu    sync.Mutex
	mkcol []string
	put   []string
}

func (d *davStub) handler(w http.ResponseWriter, r *http.Request) {
	d.mu.Lock()
	switch r.Method {
	case "MKCOL":
		d.mkcol = append(d.mkcol, r.URL.Path)
	case "PUT":
		d.put = append(d.put, r.URL.Path)
	}
	d.mu.Unlock()
	w.WriteHeader(http.StatusCreated)
}

func newDavServer(t *testing.T) (*davStub, string) {
	t.Helper()
	stub := &davStub{}
	srv := httptest.NewServer(http.HandlerFunc(stub.handler))
	t.Cleanup(srv.Close)
	return stub, srv.URL
}

func tempReportFile(t *testing.T, name string) string {
	t.Helper()
	p := JoinPath(t.TempDir(), name)
	if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
		t.Fatalf("写临时文件失败：%v", err)
	}
	return p
}

// TestUploadReportTemplatePath 按模板上传：目录逐级 MKCOL，PUT 落在解析后的目录下
func TestUploadReportTemplatePath(t *testing.T) {
	withQuietLogger(t)
	stub, url := newDavServer(t)
	file := tempReportFile(t, "巡检周报.docx")
	now := time.Now()

	cfg := &Config{
		WebdavHost: url, WebdavLogin: "u", WebdavPass: "p",
		Engineer:  "张三",
		WebdavDir: "/{REPORT_ENGINEER}/泸州市司法局/{year}/{month}/",
	}
	if err := UploadReport(cfg, file); err != nil {
		t.Fatalf("上传失败：%v", err)
	}

	wantDir := "/张三/泸州市司法局/" + now.Format("2006") + "/" + now.Format("01")
	if len(stub.put) != 1 || stub.put[0] != wantDir+"/巡检周报.docx" {
		t.Errorf("PUT 路径 = %v，期望 [%s]", stub.put, wantDir+"/巡检周报.docx")
	}
	// 目录必须逐级创建（含最后一级目录本身）
	for _, d := range []string{"/张三", "/张三/泸州市司法局", wantDir} {
		found := false
		for _, m := range stub.mkcol {
			if m == d {
				found = true
			}
		}
		if !found {
			t.Errorf("缺少 MKCOL %s，实际：%v", d, stub.mkcol)
		}
	}
}

// TestUploadReportDefaultPath 未配模板时沿用 report/<工程师>/<yyyymmdd>/ 原逻辑
func TestUploadReportDefaultPath(t *testing.T) {
	withQuietLogger(t)
	stub, url := newDavServer(t)
	file := tempReportFile(t, "a.docx")

	cfg := &Config{WebdavHost: url, Engineer: "李四"}
	if err := UploadReport(cfg, file); err != nil {
		t.Fatalf("上传失败：%v", err)
	}
	want := "/report/李四/" + time.Now().Format("20060102") + "/a.docx"
	if len(stub.put) != 1 || stub.put[0] != want {
		t.Errorf("PUT 路径 = %v，期望 [%s]", stub.put, want)
	}
}

// TestUploadReportTemplateEngineerEmpty 工程师为空时模板里的空段被折叠
func TestUploadReportTemplateEngineerEmpty(t *testing.T) {
	withQuietLogger(t)
	stub, url := newDavServer(t)
	file := tempReportFile(t, "b.docx")

	cfg := &Config{WebdavHost: url, WebdavDir: "/{REPORT_ENGINEER}/泸州市司法局/{year}/"}
	if err := UploadReport(cfg, file); err != nil {
		t.Fatalf("上传失败：%v", err)
	}
	want := "/泸州市司法局/" + time.Now().Format("2006") + "/b.docx"
	if len(stub.put) != 1 || stub.put[0] != want {
		t.Errorf("PUT 路径 = %v，期望 [%s]", stub.put, want)
	}
}
