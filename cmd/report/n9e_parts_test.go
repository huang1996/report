package main

// n9e_parts_test.go —— 「磁盘分区使用明细」相关逻辑的单元测试。
//
// 背景：报告表 5 需要逐挂载点列出真实分区。n9e 里的 path 标签混有大量噪音——
// Docker 运行时的 /run/docker/runtime-runc/moby/<id>/runc.xxxxxx（单个数据源可达上百条）、
// /sys/firmware/efi/efivars 等内核虚拟目录，以及 tmpfs / overlay 这类内存文件系统。
// 本文件锁定 isRealMount 与 diskPartsOf 的过滤与聚合行为。

import (
	"math"
	"testing"
)

// TestIsRealMount 真实分区判定：伪文件系统与运行时虚拟目录应被剔除。
func TestIsRealMount(t *testing.T) {
	real := []struct{ path, fstype string }{
		{"/", "ext4"},
		{"/data", "xfs"},
		{"/mnt", "ext4"},
		{"/tmp", "ext4"},
		{"/boot", "ext4"},
		{"/opt", "ext4"},
		{`\C:`, "NTFS"},
		{`\D:`, "NTFS"},
		{`C:`, "NTFS"},
		{"/nfs/data", "nfs4"}, // 共享存储是真实挂载点，本表保留
	}
	for _, c := range real {
		if !isRealMount(c.path, c.fstype) {
			t.Errorf("isRealMount(%q, %q) = false，应为 true", c.path, c.fstype)
		}
	}

	pseudo := []struct{ path, fstype string }{
		{"/run/docker/runtime-runc/moby/5ecd84248254015f80595e234e0eb8b1fd6c16f06800c155e92501252d92865a/runc.vit9ja", "ext4"},
		{"/run", "tmpfs"},
		{"/run/media/usb", "ext4"},
		{"/dev", "devtmpfs"},
		{"/dev/shm", "tmpfs"},
		{"/sys/firmware/efi/efivars", "efivarfs"},
		{"/proc/sys/fs/binfmt_misc", "binfmt_misc"},
		{"/var/lib/docker", "ext4"},
		{"/var/lib/docker/containers", "ext4"},
		{"/var/lib/docker/overlay2", "overlay"},
		{"/snap/core20/1974", "squashfs"},
		{"/tmp", "tmpfs"}, // 同为 /tmp，内存文件系统仍应剔除
		{"/", "overlay"},
		{"/anything", "squashfs"},
		{"?", ""},
		{"", "ext4"},
	}
	for _, c := range pseudo {
		if isRealMount(c.path, c.fstype) {
			t.Errorf("isRealMount(%q, %q) = true，应为 false", c.path, c.fstype)
		}
	}
}

// TestDiskPartsOf 分区明细：过滤伪分区、按挂载点排序、使用率取周期均值。
func TestDiskPartsOf(t *testing.T) {
	vols := map[string]diskVol{
		"/":    {device: "dm-0", fstype: "xfs", total: 999.51 * testGB},
		"/mnt": {device: "vdb1", fstype: "ext4", total: 492.03 * testGB},
		"/run/docker/runtime-runc/moby/abc/runc.1a2b3c": {device: "tmpfs", fstype: "tmpfs", total: 0.06 * testGB},
		"/sys/firmware/efi/efivars":                     {device: "efivarfs", fstype: "efivarfs", total: 0.0},
		"/data/docker/containers":                       {device: "dm-1", fstype: "xfs", total: 100 * testGB},
	}
	seq := map[string][][2]float64{
		"/":                       {{0, 30}, {60, 34}}, // 均值 32
		"/mnt":                    {{0, 10}, {60, 20}}, // 均值 15
		"/data/docker/containers": {{0, 50}, {60, 50}},
	}

	got := diskPartsOf(vols, seq)
	if len(got) != 3 {
		t.Fatalf("分区数 = %d，期望 3（%v）", len(got), got)
	}
	// 按挂载点排序
	wantOrder := []string{"/", "/data/docker/containers", "/mnt"}
	for i, p := range got {
		if p.Path != wantOrder[i] {
			t.Errorf("第 %d 个挂载点 = %q，期望 %q", i, p.Path, wantOrder[i])
		}
	}
	if math.Abs(got[0].UsedPct-32) > 1e-9 {
		t.Errorf("/ 使用率 = %.2f，期望 32（周期均值）", got[0].UsedPct)
	}
	if math.Abs(got[0].CapGB-999.51) > 0.01 {
		t.Errorf("/ 容量 = %.2f GB，期望 999.51 GB", got[0].CapGB)
	}
	if got[0].Fstype != "xfs" || got[1].Fstype != "xfs" {
		t.Errorf("文件系统类型未透传：%v", got)
	}
	if math.Abs(got[2].CapGB-492.03) > 0.01 {
		t.Errorf("/mnt 容量 = %.2f GB，期望 492.03 GB", got[2].CapGB)
	}
}

// TestDiskPartsOfDedupesSameDevice 同一设备挂多个路径时只保留一个代表挂载点，
// 避免同一块盘在明细里出现多行重复数据（实测 ds28 的 dm-0 同挂 /data 与 /mnt/arkbase_backups*）。
func TestDiskPartsOfDedupesSameDevice(t *testing.T) {
	vols := map[string]diskVol{
		"/":                     {device: "vda3", fstype: "ext4", total: 38.09 * testGB},
		"/data":                 {device: "dm-0", fstype: "xfs", total: 999.51 * testGB},
		"/mnt/arkbase_backups":  {device: "dm-0", fstype: "xfs", total: 999.51 * testGB},
		"/mnt/arkbase_backups2": {device: "dm-0", fstype: "xfs", total: 999.51 * testGB},
	}
	seq := map[string][][2]float64{
		"/":     {{0, 20}},
		"/data": {{0, 15.37}},
	}
	got := diskPartsOf(vols, seq)
	if len(got) != 2 {
		t.Fatalf("分区数 = %d，期望 2（同一设备只留一个代表）：%v", len(got), got)
	}
	// 容量相同时取路径更短者，故 /data 胜出
	var paths []string
	for _, p := range got {
		paths = append(paths, p.Path)
	}
	if paths[0] != "/" || paths[1] != "/data" {
		t.Errorf("代表挂载点 = %v，期望 [/ /data]", paths)
	}
}

// TestBetterDiskRep 代表挂载点选择必须确定：容量 > 路径长度 > 字典序。
func TestBetterDiskRep(t *testing.T) {
	big := diskRep{path: "/mnt/long/path", total: 500}
	small := diskRep{path: "/data", total: 100}
	if !betterDiskRep(big, small) {
		t.Error("容量大者应胜出")
	}
	if betterDiskRep(small, big) {
		t.Error("容量小者不应胜出")
	}
	short := diskRep{path: "/data", total: 500}
	long := diskRep{path: "/mnt/arkbase_backups", total: 500}
	if !betterDiskRep(short, long) {
		t.Error("容量相同时路径更短者应胜出")
	}
	a := diskRep{path: "/aaa", total: 500}
	b := diskRep{path: "/bbb", total: 500}
	if !betterDiskRep(a, b) || betterDiskRep(b, a) {
		t.Error("容量与长度都相同时应按字典序稳定取舍")
	}
}

// TestDiskPartsOfKeepsRemoteFS 共享存储同样是会写满的挂载点，明细表予以保留。
func TestDiskPartsOfKeepsRemoteFS(t *testing.T) {
	vols := map[string]diskVol{
		"/":                          {device: "vda1", fstype: "ext4", total: 39.25 * testGB},
		"/opt/middleware/minio/data": {device: "efs.hlw:/share_8adc", fstype: "nfs", total: 102400 * testGB},
	}
	got := diskPartsOf(vols, nil)
	if len(got) != 2 {
		t.Fatalf("分区数 = %d，期望 2（含 NFS）：%v", len(got), got)
	}
	if got[1].Fstype != "nfs" {
		t.Errorf("NFS 挂载点未被保留：%v", got)
	}
}

// TestDiskPartsOfNoData 无挂载点数据时返回空（调用方据此跳过表格）。
func TestDiskPartsOfNoData(t *testing.T) {
	if got := diskPartsOf(nil, nil); got != nil {
		t.Errorf("应为 nil，实得 %v", got)
	}
	if got := diskPartsOf(map[string]diskVol{}, nil); len(got) != 0 {
		t.Errorf("应为空切片，实得 %v", got)
	}
}

// TestDiskPartsOfSkipsPseudoOnly 整机挂载点全是伪分区时，明细为空。
func TestDiskPartsOfSkipsPseudoOnly(t *testing.T) {
	vols := map[string]diskVol{
		"/dev/shm": {device: "tmpfs", fstype: "tmpfs", total: 3.9 * testGB},
		"/run":     {device: "tmpfs", fstype: "tmpfs", total: 3.9 * testGB},
	}
	if got := diskPartsOf(vols, nil); len(got) != 0 {
		t.Errorf("伪分区不应进入明细，实得 %v", got)
	}
}
