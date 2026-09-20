# 巡检周报生成器（Go 合并版）

> ## ⚠️ 本项目由纯 AI 生成 ⚠️
>
> **本项目（全部 Go 代码、Dockerfile、docker-compose、文档）100% 由 AI（WorkBuddy 智能体）自动生成，未经人工代码审查。**
> 使用前请务必：① 审查代码逻辑与安全性；② 在测试环境充分验证后再上生产；③ 自行承担部署风险。

将「云资源巡检周报」（夜莺 n9e 数据源）与 [safeline-report](https://github.com/huang1996/safeline-report)（雷池 WAF 巡检）两套脚本合并为一个 Go 程序：**输出一份同时包含资源巡检与 WAF 安全巡检的 docx 周报**。

- 报告格式与主体结构以原「生成巡检周报」脚本为准（章节、图表、阈值口径、配色一致）
- safeline-report 的全部数据（防护应用、访问/攻击统计、攻击类型饼图、未拦截明细）无缺失融入 WAF 章节
- WebDAV 上传逻辑保留（`report/<工程师>/<yyyymmdd>/<文件名>`）
- Go 重构，无 Python 依赖；docker compose 一键部署，配置走 `.env`，命令参数走 compose `command`

## 报告结构（合并后）

| 章节 | 内容 | 来源 |
|---|---|---|
| 1、报告信息 | 报告名称 / 周期 / 对象 | 巡检周报 |
| 2、本周总体结论 | 综合评级 + 资源结论 + WAF 拦截概览 | 两者融合 |
| 2.1 关键指标概览 | CPU / 内存 / 磁盘 / 带宽 / 连接数 KPI 表 | 巡检周报 |
| 3、资源水位分析 | 图 1~5（当前 vs 峰值、IO、峰谷差、总览） | 巡检周报 |
| 4、巡检明细 | 表 2~4（资源明细 / 归属规格 / 性能网络） | 巡检周报 |
| 5、业务巡检 | 留空，人工填写（`-report_include_biz` 开启） | 巡检周报 |
| 6、Web 应用防火墙安全巡检 | 6.1 防护应用概览 / 6.2 访问统计（地域、IP TOP10）/ 6.3 攻击统计（类型饼图、IP TOP10）/ 6.4 未拦截攻击明细 | safeline-report |
| 7、风险研判与优化建议 | 资源风险 + WAF 未拦截风险（自动合并） | 两者融合 |
| 8、下周巡检重点 | 计划表（含 WAF 处置项） | 两者融合 |

## Docker Compose 部署示例

```yaml
# docker-compose.yml
services:
  report:
    build: .
    image: report:latest
    container_name: inspection-report
    restart: unless-stopped          # 常驻运行（定时模式必需）
    env_file:
      - .env                         # 所有服务器/数据库/账号配置都在 .env
    volumes:
      - ./report:/app/report         # 报告输出目录挂载到宿主机
    # ===== 命令参数在此自定义（参数名与 .env 环境变量同名小写，见下方对照表）=====
    # 示例 1：常驻定时模式 —— 每天 12:00 生成最近 7 个完整天（前七天）的滚动报告
    command: ["-period", "days", "-n9e_ds_id", "20", "-report_engineer", "张三"]
    # 示例 2：每周五 12:00 生成上周五 ~ 本周四的报告
    # command: ["-period", "fri", "-report_run_weekdays", "5"]
    # 示例 3：单次立即生成指定周期报告后退出（配合 docker compose run --rm 使用更佳）
    # command: ["-now", "-period", "week", "-start", "2026-09-07", "-end", "2026-09-13"]
```

```bash
# 部署步骤
cp .env.example .env       # 填入 n9e / WAF 数据库 / WebDAV 配置
docker compose build
docker compose up -d       # 常驻定时模式
docker compose logs -f     # 查看日志

# 单次立即生成
docker compose run --rm report -now -start 2026-09-07 -end 2026-09-13

# 查看 n9e 数据源清单
docker compose run --rm report -list-ds
```

## 参数 × 环境变量 对照表

**优先级：命令参数显式传入 > 环境变量 > 内置默认值。**
双通道配置项命名规则：**环境变量大写、命令参数同名小写**（如 `N9E_DS_ID` ↔ `-n9e_ds_id`）。

| 环境变量 | 命令参数 | 说明 | 默认值 |
|---|---|---|---|
| `N9E_BASE` | `-n9e_base` | 夜莺 n9e 服务地址；**留空则不含资源巡检数据** | |
| `N9E_DS_ID` | `-n9e_ds_id` | 数据源 ID（一个数据源≈一个项目） | `1` |
| `N9E_TOKEN` | `-n9e_token` | n9e 个人令牌（免认证部署可留空） | |
| `N9E_USER` | `-n9e_user` | n9e 登录账号（与 `N9E_PASS` 配合） | |
| `N9E_PASS` | `-n9e_pass` | n9e 登录密码 | |
| `N9E_PROJECT` | `-n9e_project` | 项目名关键字：ident 含该关键字的主机才纳入统计；未指定 `N9E_DS_ID` 时按它在全部数据源中检索 | |
| `N9E_MAX_DS` | `-n9e_max_ds` | 自动检索数据源时的最大编号 | `90` |
| `REPORT_NAME` | `-report_name` | 自定义报告名称；留空则按单一业务系统自动生成「XX巡检周报」 | |
| `REPORT_ENGINEER` | `-report_engineer` | 运维工程师，一处配置三处生效：① 覆盖各主机工程师 ② 报告信息表 ③ WebDAV 上传目录名（为空时上传到 `report/default/`） | |
| `WAF_DATABASE_URL` | `-waf_database_url` | Safeline PostgreSQL 连接串；**留空则跳过 WAF 章节**（旧名 `DATABASE_URL` 兼容） | |
| `WAF_EXCEPT_APP_IDS` | `-waf_except_app_ids` | 排除的 WAF 应用 ID，逗号分隔 | |
| `WAF_EXCEPT_IPS` | `-waf_except_ips` | 排除的 WAF 访问来源 IP，逗号分隔 | |
| `REPORT_DIR` | `-report_dir` | 报告输出目录 | `/app/report` |
| `REPORT_TIME` | `-report_time` | 定时模式每日触发时刻 `HH:MM` | `12:00` |
| `REPORT_HEADER` | `-report_header` | 报告页眉文字（每页顶部居中显示） | `统筹运维项目` |
| `REPORT_INCLUDE_BIZ` | `-report_include_biz` | 是否添加「业务巡检」章节（`1`/`true` 开启） | 不添加 |
| `REPORT_FAKE_WHEN_EMPTY` | `-fake_when_empty` | 统计周期内 n9e 无数据时，按数据源最新数据随机增减伪造资源巡检指标（详见下节） | 关闭 |
| `REPORT_RUN_WEEKDAYS` | `-report_run_weekdays` | 定时模式下仅在这些星期生成（`0`=周日…`6`=周六，逗号分隔） | 每天 |
| `LOG_LEVEL` | `-log_level` | 日志等级：DEBUG / INFO / WARN / ERROR | `INFO` |
| `WEBDAV_HOSTNAME` | `-webdav_hostname` | WebDAV 上传地址；**不配则只存本地** | |
| `WEBDAV_LOGIN` | `-webdav_login` | WebDAV 账号 | |
| `WEBDAV_PASSWORD` | `-webdav_password` | WebDAV 密码 | |

### 仅命令行参数（无对应环境变量）

| 命令参数 | 说明 | 默认值 |
|---|---|---|
| `-period` | 周期口径：`check`=巡检周（周六~周五）/ `week`=自然周（周一~周日）/ `fri`=周五~次周四 / `days`=最近 N 个完整天（滚动）/ `range`=`-start`~`-end` 整段一份 | `check` |
| `-start` | 起始日期 `YYYY-MM-DD` | |
| `-end` | 结束日期 `YYYY-MM-DD`；只指定 `-end` 时往前推 `-days` 天 | |
| `-days` | 往前推的天数；`-period days` 时为滚动窗口天数 | `7` |
| `-step` | Prometheus 采样步长（秒） | `300` |
| `-layout` | ident 命名解析规则：`auto` / `section-first` / `project-first` | `auto` |
| `-split` | 报告拆分维度：`none` / `project` / `section` | `none` |
| `-chart-top` | 图表最多展示的主机台数，`0`=全部 | `18` |
| `-out` | 输出 docx 路径（仅单份报告时生效） | |
| `-dump` | 只打印采集到的数据，不生成报告 | |
| `-list-ds` | 列出 n9e 全部数据源清单后退出 | |
| `-insecure` | 跳过 HTTPS 证书校验 | |
| `-now` | 立即执行一次后退出（否则常驻定时执行） | |
| `-demo` | 使用内置样例数据生成报告（本地验证） | |
| `-version` | 打印版本信息后退出 | |

> **布尔参数的写法**：`-flag`、`-flag=true`、`-flag true` 三种都支持
> （启动时会把 `-flag true` 这类写法统一归一化，避免标准库在 `true` 处提前终止参数解析、
> 导致其后的参数被静默忽略）。无法识别的参数会以 `WARN` 级别明确提示。

## 周期内无数据时的伪造数据（`-fake_when_empty`）

当巡检周期内 n9e 采集不到数据（采集中断、数据源刚接入无历史数据、周期尚未产生数据等），
默认会打印「本周期无可用数据，跳过」并且不生成报告。加上 `-fake_when_empty` 后，
将以该数据源的**最新数据**为基准随机增减，伪造出本周期的资源巡检指标，使报告仍能完整生成。

```bash
# 周期内无数据时用最新数据伪造
docker compose run --rm report -now -fake_when_empty
```

**工作机制**

1. 先判定周期内是否真的无数据（采集失败 / 无主机 / 各项指标与采样率全为 0 均视为无数据）；
2. 查询该数据源**最近 1 小时**的数据作为基准（仅保存在内存中，**不写入磁盘**；
   一次运行补齐多个周期时共用同一份基准，不会每个周期重复查询）；
3. 以基准主机列表为母本，对使用率类指标做随机增减（当前值 ±15%、峰值 ±25%，越接近 100% 上行空间越小，
   不会出现整 100% 的假值），峰值不低于当前值；
4. 基准查询失败或未取到有效数据时**不做伪造**，该周期仍按「无可用数据，跳过」处理。

**边界**

- **仅作用于 n9e 资源巡检指标**；WAF（safeline）章节的数据与判定完全不受影响；
- 硬件规格（核数 / 内存容量 / 磁盘容量）与归属信息（网络分区 / 业务系统 / 主机角色 / 操作系统 / 工程师）
  保持基准原值，不做伪造；
- 伪造的数值**不可用于真实结论**，日志会以 WARN 级别明确提示；报告正文不做标注，使用前请自行确认。

## CI/CD：版本发布与 Docker 镜像分发

推送 `v` 开头的标签（如 `v0.1.0`）即自动触发 GitHub Actions（`.github/workflows/docker-release.yml`）：

1. **多架构镜像构建**：使用 Buildx + QEMU 构建 `linux/amd64` + `linux/arm64` 双架构镜像
2. **推送到 Docker Hub**：`huangtao1996/report:<版本号>` + `huangtao1996/report:latest`，版本号同时注入二进制（`-version` 可见）
3. **交叉编译二进制**：产出 linux/amd64、linux/arm64、windows/amd64 三个平台的压缩包
4. **创建 GitHub Release**：自动汇总自上一标签以来的提交作为版本更新说明，二进制压缩包作为 Release 附件上传，正文附镜像拉取命令与附件平台对照表

**发版步骤**

```bash
git tag v0.2.0
git push origin v0.2.0     # 触发构建，几分钟后镜像即可在 Docker Hub 拉取
```

**首次使用前配置**（仓库 Settings → Secrets and variables → Actions）：

| Secret | 说明 |
|---|---|
| `DOCKERHUB_USERNAME` | Docker Hub 用户名 |
| `DOCKERHUB_TOKEN` | Docker Hub Access Token（Docker Hub → Account Settings → Security 中创建，非登录密码） |

## 与原脚本的差异说明

- **Excel 数据源未迁移**：原巡检周报脚本的 Excel 兼容模式已移除（docker 场景固定走 n9e + WAF 数据库），如需恢复可后续补充
- 第 5 章「业务巡检」按原设计留空人工填写（默认不添加）；原脚本中未启用的「业务可用性自动分析」未迁移
- WAF 访问地域 / 攻击时间等字段做了可读化格式（时间戳转日期时间）
- 单项 WAF 查询失败不会中断整份报告，会在对应章节注明原因并在日志中记录
- 图表由纯 Go 渲染（freetype + 自绘柱状/饼图）；**Noto Sans SC 字体内嵌在二进制中**（OFL 开源协议），不依赖容器/系统字体，任何环境文字均可渲染
- 报告文件名格式：`报告名称_巡检开始日期_巡检结束日期.docx`（如 `巡检周报_20260912_20260918.docx`）

## 项目结构

```
report/                  # 项目根目录（Go module）
├── cmd/report/          # Go 源码（入口 main.go 及各功能模块）
│   ├── main.go          # 入口：调度、周期窗口解析
│   ├── config.go        # 配置：env + 命令参数双通道
│   ├── n9e.go           # 夜莺 n9e 采集（Prometheus 代理接口）
│   ├── waf.go           # Safeline WAF PostgreSQL 采集
│   ├── analyze.go       # 资源数据分析与风险研判
│   ├── charts.go        # 图表绘制（freetype 自绘柱状图/饼图）
│   ├── fonts/           # 内嵌中文字体 NotoSansSC-Regular.ttf（OFL 协议）
│   ├── docx.go          # OOXML docx 生成器（三线表/图表/样式）
│   ├── build.go         # 报告组装（章节、表格、文案）
│   ├── webdav.go        # WebDAV 上传
│   ├── logger.go        # 日志
│   └── version.go       # 版本信息
├── report/              # 报告输出目录（运行时生成，已 gitignore；归档/ 存历史版本）
├── logs/                # 运行日志（已 gitignore）
├── go.mod / go.sum
├── Dockerfile           # 多阶段构建（内置中文字体）
├── docker-compose.yml   # 部署编排（env_file + command）
└── .env.example         # 环境变量模板
```

## 本地开发

```bash
go build ./...                          # 编译
go run ./cmd/report -version            # 查看版本
go run ./cmd/report -demo -now -out 输出/测试.docx   # 样例数据验证
```
