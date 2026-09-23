package main

// n9e_disk_test.go —— 磁盘容量/使用率聚合口径的单元测试。
//
// 背景：磁盘容量原先取「使用率最高的那个分区」的容量，多挂载点主机会严重低估
// （典型如 10.82.9.9：/ 38 G + /mnt 492 G，却只统计了 38 G）。现改为全部本地
// 挂载点之和，使用率同步改为按容量加权。本文件锁定这两个函数的边界行为。

import (
	"math"
	"testing"
)

const testGB = float64(1 << 30)

// TestPickDiskReps 覆盖容量归并的各类形态。
func TestPickDiskReps(t *testing.T) {
	cases := []struct {
		name     string
		vols     map[string]diskVol
		wantCap  float64 // GB
		wantReps int
	}{
		{
			name: "多挂载点求和：10.82.9.9 的 / 与 /mnt",
			vols: map[string]diskVol{
				"/":    {device: "vda2", fstype: "ext4", total: 38.26 * testGB},
				"/mnt": {device: "vdb1", fstype: "ext4", total: 492.03 * testGB},
			},
			wantCap:  530.29,
			wantReps: 2,
		},
		{
			name: "同一设备挂多个路径只计一次（dm-0 同时挂三处）",
			vols: map[string]diskVol{
				"/":                     {device: "vda3", fstype: "ext4", total: 38.09 * testGB},
				"/data":                 {device: "dm-0", fstype: "xfs", total: 999.51 * testGB},
				"/mnt/arkbase_backups":  {device: "dm-0", fstype: "xfs", total: 999.51 * testGB},
				"/mnt/arkbase_backups2": {device: "dm-0", fstype: "xfs", total: 999.51 * testGB},
			},
			wantCap:  1037.60,
			wantReps: 2,
		},
		{
			name: "排除 NFS 共享存储（100 TB 数据卷不计入）",
			vols: map[string]diskVol{
				"/":                          {device: "vda1", fstype: "ext4", total: 39.25 * testGB},
				"/opt":                       {device: "dm-1", fstype: "ext4", total: 58.93 * testGB},
				"/opt/middleware/minio/data": {device: "efs.hlw:/share_8adc", fstype: "nfs", total: 102400 * testGB},
			},
			wantCap:  98.18,
			wantReps: 2,
		},
		{
			name: "Windows 盘符按 device 求和",
			vols: map[string]diskVol{
				`\C:`: {device: "C:", fstype: "NTFS", total: 100 * testGB},
				`\D:`: {device: "D:", fstype: "NTFS", total: 900 * testGB},
				`\E:`: {device: "E:", fstype: "NTFS", total: 200 * testGB},
			},
			wantCap:  1200,
			wantReps: 3,
		},
		{
			name: "整机只有 NFS 时回退为全部计入，容量不显示 0",
			vols: map[string]diskVol{
				"/nfs/data": {device: "192.168.1.231:/edata/k3s/nfs", fstype: "nfs4", total: 983.2 * testGB},
			},
			wantCap:  983.2,
			wantReps: 1,
		},
		{
			name: "device 为空时退化为按 path 归并",
			vols: map[string]diskVol{
				"/a": {device: "", fstype: "ext4", total: 10 * testGB},
				"/b": {device: "", fstype: "ext4", total: 20 * testGB},
			},
			wantCap:  30,
			wantReps: 2,
		},
		{
			name:     "无挂载点",
			vols:     map[string]diskVol{},
			wantCap:  0,
			wantReps: 0,
		},
	}

	for _, c := range cases {
		got, reps := pickDiskReps(c.vols)
		if math.Abs(got/testGB-c.wantCap) > 0.01 {
			t.Errorf("%s：容量 = %.2f GB，期望 %.2f GB", c.name, got/testGB, c.wantCap)
		}
		if len(reps) != c.wantReps {
			t.Errorf("%s：代表挂载点 %d 个，期望 %d 个（%v）", c.name, len(reps), c.wantReps, reps)
		}
	}
}

// TestPickDiskRepsSameDeviceKeepsLargest 同一设备多挂载点时保留容量最大的那个作为代表。
func TestPickDiskRepsSameDeviceKeepsLargest(t *testing.T) {
	vols := map[string]diskVol{
		"/small": {device: "vdb", fstype: "xfs", total: 100 * testGB},
		"/large": {device: "vdb", fstype: "xfs", total: 500 * testGB},
	}
	cap, reps := pickDiskReps(vols)
	if math.Abs(cap/testGB-500) > 0.01 {
		t.Errorf("容量 = %.2f GB，期望 500 GB", cap/testGB)
	}
	if _, ok := reps["/large"]; !ok || len(reps) != 1 {
		t.Errorf("应保留容量最大的 /large 作为代表，实得 %v", reps)
	}
}

// TestPickDiskRepsSameDeviceTieBreak 同一设备多挂载点且容量相同时（bind mount 的典型形态），
// 必须稳定选中路径最短的那个——否则 map 遍历顺序随机会让每次运行结果漂移。
func TestPickDiskRepsSameDeviceTieBreak(t *testing.T) {
	vols := map[string]diskVol{
		"/data":                 {device: "dm-0", fstype: "xfs", total: 999.51 * testGB},
		"/mnt/arkbase_backups":  {device: "dm-0", fstype: "xfs", total: 999.51 * testGB},
		"/mnt/arkbase_backups2": {device: "dm-0", fstype: "xfs", total: 999.51 * testGB},
	}
	for i := 0; i < 20; i++ { // 多跑几轮，暴露 map 遍历顺序带来的不确定性
		capBytes, reps := pickDiskReps(vols)
		if math.Abs(capBytes/testGB-999.51) > 0.01 {
			t.Fatalf("容量 = %.2f GB，期望 999.51 GB", capBytes/testGB)
		}
		if len(reps) != 1 {
			t.Fatalf("代表挂载点 %d 个，期望 1 个（%v）", len(reps), reps)
		}
		if _, ok := reps["/data"]; !ok {
			t.Fatalf("应稳定选中路径最短的 /data，实得 %v", reps)
		}
	}
}

// TestWeightedDisk 使用率按容量加权，与「容量 = 全部挂载点合计」保持同一口径。
func TestWeightedDisk(t *testing.T) {
	reps := map[string]float64{
		"/":    100 * testGB,
		"/mnt": 900 * testGB,
	}
	seq := map[string][][2]float64{
		"/":    {{0, 80}, {60, 80}},
		"/mnt": {{0, 10}, {60, 20}},
	}
	// t=0 : (100×80 + 900×10) / 1000 = 17
	// t=60: (100×80 + 900×20) / 1000 = 26
	got := weightedDisk(reps, seq)
	if len(got) != 2 {
		t.Fatalf("时间点数 = %d，期望 2", len(got))
	}
	if math.Abs(got[0]-17) > 1e-9 || math.Abs(got[1]-26) > 1e-9 {
		t.Errorf("加权使用率 = %v，期望 [17 26]", got)
	}
}

// TestWeightedDiskPartialData 某挂载点缺某时刻数据时，仅对有数据的部分加权。
func TestWeightedDiskPartialData(t *testing.T) {
	reps := map[string]float64{"/": 100 * testGB, "/mnt": 900 * testGB}
	seq := map[string][][2]float64{
		"/":    {{0, 50}},
		"/mnt": {{0, 10}, {60, 30}},
	}
	got := weightedDisk(reps, seq)
	if len(got) != 2 {
		t.Fatalf("时间点数 = %d，期望 2", len(got))
	}
	// t=0: (100×50 + 900×10) / 1000 = 14
	if math.Abs(got[0]-14) > 1e-9 {
		t.Errorf("t0 = %.4f，期望 14", got[0])
	}
	// t=60: 只有 /mnt 有数据 → 30
	if math.Abs(got[1]-30) > 1e-9 {
		t.Errorf("t60 = %.4f，期望 30", got[1])
	}
}

// TestWeightedDiskNoReps 无代表挂载点（无任何磁盘数据）时返回空序列。
func TestWeightedDiskNoReps(t *testing.T) {
	if got := weightedDisk(map[string]float64{}, map[string][][2]float64{}); len(got) != 0 {
		t.Errorf("应为空序列，实得 %v", got)
	}
}

// TestIsRemoteFS 网络/共享文件系统判定。
func TestIsRemoteFS(t *testing.T) {
	remote := []string{"nfs", "nfs4", "NFS", " nfs4 ", "cifs", "smbfs", "smb3",
		"fuse.s3fs", "ceph", "glusterfs", "9p", "sshfs", "davfs"}
	for _, fs := range remote {
		if !isRemoteFS(fs) {
			t.Errorf("isRemoteFS(%q) = false，应为 true", fs)
		}
	}
	local := []string{"ext4", "xfs", "NTFS", "ext3", "vfat", "btrfs", "", "fuseblk", "overlay", "tmpfs"}
	for _, fs := range local {
		if isRemoteFS(fs) {
			t.Errorf("isRemoteFS(%q) = true，应为 false", fs)
		}
	}
}
