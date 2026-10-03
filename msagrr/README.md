# msagrr — 量具测量系统分析（Gauge R&R）后端服务

平板录入、服务端计算的量具 R&R（GRR）服务。一次研究绑定**一台量具、一个特性及其公差**，
对**零件 × 测量人 × 重复次数**三因素交叉设计做 **方差分析（ANOVA）法** GRR。

- Go 1.23 + Gin；嵌入式存储 [bbolt](https://github.com/etcd-io/bbolt)（单文件、ACID、零外部依赖）
- 数据文件位于挂载卷 `MSA_DATA_DIR`（默认 `/data`），**重启后数据、版本、结果完整**
- 服务**只提供 HTTP/JSON 接口**，无 UI
- 不引入任何统计库：F 分布 p 值由本仓库自行实现（正则化不完全贝塔函数 + 连分式）

---

## 1. 核心概念

```
Study（研究：量具 + 特性 + 公差）
 └─ Version 1  [open]            进行中：可增删改测量值
     ├─ Measurement ...          每条带 part / operator / trial / value / recorded_at
     └─ Result (指纹 F1)         结果绑定其依据的数据指纹
        定稿 → [finalized] 冻结：任何写入返回 409
 └─ Version 2  [open]  (返测)    基于已定稿版本克隆原始读数开新版本
     └─ Result ...               旧版本的数据与结果原样保留、随时可查
```

- **进行中 / 已定稿**：进行中版本可增删改；定稿（finalize）即冻结。定稿版本的任何写请求
  一律 `409 version_finalized`，要改只能基于它开返测新版本。
- **版本与返测**：`POST /studies/{sid}/versions/{vid}/return` 克隆指定定稿版本的全部原始读数，
  生成新的 open 版本（`parent_version_id` 指回旧版）。旧版本数据、结果永不修改。
- **结果不过期不覆盖**：每次计算都新建 Result，并绑定当时数据的 SHA-256 指纹。
  数据一旦变化，旧结果在查询时标记 `stale: true`（而非被删除或覆盖），审计可追溯。
- **乐观并发**：测量值带单调递增 `revision`。两个平板同时改同一条：后到者用旧 revision
  更新会收到 `409 revision_conflict`，不会静默覆盖。

---

## 2. 非平衡数据怎么算（默认方法与理由）

现场真正的痛点是缺测：漏测几次、某个零件只被部分人测过、各单元重复次数不同。

本服务默认采用**广义完整单元法（complete-case）→ 最大平衡子设计 → 标准交叉双因素 ANOVA**：

1. **剔除不完整零件**：只保留“被每一位测量人都测过”的零件；只被部分人测过的零件整个剔除。
2. **对齐重复次数**：在保留下来的零件×测量人单元中，取各单元“次序号（trial）集合”的**交集**，
   保证每个单元重复次数相同（R）；多出的重复读数剔除。
3. 对得到的 **P × O × R 完全平衡设计**跑教科书式交叉双因素 ANOVA（含交互）。

**为什么默认选它，而不是直接上非平衡估计（如 EMS/SAS Type I–III、ML/REML）：**

- 审核场景要的是**可解释、与标准一致**的结果。AIAG MSA 手册的 GRR-ANOVA 本身定义在平衡交叉
  设计上；先归整为最大平衡子设计，期望均方（EMS）对方差分量的识别保持**精确**，
  **平衡数据无论走哪条入口都与标准 ANOVA 完全一致**（回归测试锁定）。
- 非平衡设计的不同“平方和类型”（Type I/II/III）在含交互时会给出不同答案，口径选择本身需要
  额外解释，且其结果不等于客户熟悉的平衡 ANOVA，审核时反而容易起争议。
- 该方法**透明地报告本次实际用了哪些数据**：结果里 `data_used` 明确列出
  - `parts / operators / trial_numbers`：最终进入分析的水平与次序号；
  - `raw_readings` / `used_readings`：原始条数 / 实际使用条数；
  - `excluded_readings`：每条被剔除读数的零件、测量人、次序号、数值与剔除原因；
  - `missing_slots`：相对于观测到的最大重复次数，缺失的零件×测量人×次序号槽位。

> 当数据完全平衡时，剔除数为 0，输出即 10×3×3（或任意 P×O×R）标准结果。

### 交互项合并与负方差分量

- 交互项 **p > 0.25**（AIAG 阈值，结果里写明阈值）时，将交互平方和/自由度并入重复性误差项
  重算，并在 `pooling.reason` 中说明；p ≤ 0.25 则保留交互为独立方差分量。
- 每个单元仅 1 次重复（R=1）时没有单元内误差，交互不可检验，交互 SS 直接作为误差项。
- 方差分量出现**负估计时按 0 处理**，并在对应分量上标注
  `negative_estimate_clamped_to_zero: true`，同时在 `notes` 中列出。

---

## 3. 输出的统计量

- ANOVA 表：零件、测量人、零件×测量人交互、重复性的 **SS、df、MS、F、p**；
- 各方差分量（重复性、再现性、交互[若保留]、零件变差）；
- 重复性、再现性、GRR、零件变差、总变差的**标准差与占总方差百分比**；
- **%GRR（相对总变差）= 100·σ_GRR / σ_TV**（6σ 在分子分母抵消，即标准差之比）；
- **%GRR（相对公差）= 100·6σ_GRR / 公差**（量具 6σ 散布，即 99.73% 区间，AIAG 模板惯例）；
- **可区分类别数 NDC = floor(1.41 · σ_part / σ_GRR)**。

> 缩放不变性：全部读数乘 c（公差同为 c 倍单位）时，两个 %GRR 与 NDC 不变；
> 公差固定不变时，%GRR/公差随 c 线性变化。回归测试同时锁定这两点。

---

## 4. HTTP 接口（/api/v1）

| 方法 | 路径 | 说明 |
|---|---|---|
| GET  | `/health` | 健康检查 |
| POST | `/studies` | 建研究（同时创建 version 1） |
| GET  | `/studies` | 列研究 |
| GET  | `/studies/{sid}` | 研究详情（含版本列表） |
| GET  | `/studies/{sid}/versions` | 列版本 |
| POST | `/studies/{sid}/versions/{vid}/measurements` | 逐条录数 |
| POST | `/studies/{sid}/versions/{vid}/measurements/batch` | 整批导入 |
| GET  | `/studies/{sid}/versions/{vid}/measurements` | 列读数 |
| GET  | `/measurements/{mid}` | 取单条 |
| PUT  | `/measurements/{mid}` | 改值（body 带 `revision` 做乐观锁） |
| DELETE | `/measurements/{mid}` | 删（可选 `If-Match: <revision>`） |
| POST | `/studies/{sid}/versions/{vid}/results` | 计算并存结果（不改状态） |
| GET  | `/studies/{sid}/versions/{vid}/results` | 列结果（含动态 stale 标记） |
| GET  | `/results/{rid}` | 取单个结果 |
| POST | `/studies/{sid}/versions/{vid}/finalize` | 定稿冻结（同时存一份结果快照） |
| POST | `/studies/{sid}/versions/{vid}/return` | 基于定稿版本开返测新版本 |

### 请求示例

建研究：
```bash
curl -XPOST localhost:8080/api/v1/studies -H 'Content-Type: application/json' -d '{
  "gauge_id":"MIC-017","gauge_name":"外径千分尺",
  "characteristic":"轴外径","tolerance":0.05
}'
```

录数（单条 / 批量）：
```bash
# 单条
curl -XPOST .../versions/v-2/measurements -d '{"part":"P01","operator":"Alice","trial":1,"value":10.02}'
# 批量
curl -XPOST .../versions/v-2/measurements/batch -d '{"readings":[
  {"part":"P01","operator":"Alice","trial":1,"value":10.02},
  {"part":"P01","operator":"Alice","trial":2,"value":10.01}]}'
```

改值（乐观锁；后到的冲突者收到 409）：
```bash
curl -XPUT .../measurements/m-31 -d '{"value":10.03,"revision":1}'
```

计算 / 定稿 / 返测：
```bash
curl -XPOST .../versions/v-2/results
curl -XPOST .../versions/v-2/finalize
curl -XPOST .../versions/v-2/return      # 返回 version 3（克隆 v-2，open）
```

### 错误与字段定位

- 校验失败返回 `400`，`error.field_errors[]` 精确到字段，批量请求带位置：
  如 `readings[3].value`、`tolerance`、`readings[1].trial`。
- 同一零件/测量人/次序号重复录入 → `409 duplicate_slot`。
- 已定稿写入 → `409 version_finalized`。
- 并发更新冲突 → `409 revision_conflict`。
- 非有限数（`NaN`、`Infinity`、`-Infinity`、溢出如 `1e999`）：服务端用宽容扫描器解析，
  即使标准 JSON 不允许这些 token，也能把错误指到具体数值字段。

校验规则：测量值必须是有限数；公差必须为正且有限；零件、测量人各至少 2 个
（不足无法做交叉 GRR，计算时返回明确错误）；同 (part, operator, trial) 唯一。

---

## 5. 运行

### 容器（两阶段构建，基于 golang:1.23-alpine）

```bash
docker build -t msagrr:latest .
docker run -p 8080:8080 -v /mnt/msa-data:/data msagrr:latest
```

环境变量：`MSA_DATA_DIR`（默认 `/data`）、`MSA_HTTP_ADDR`（默认 `:8080`）、
`GIN_MODE`（默认 release）。

### 本地

```bash
go run ./cmd/server
```

---

## 6. 测试与核对关系

```bash
go test ./... -race -count=1
```

回归测试覆盖（见 `stat/anova_test.go`、`stat/filter_test.go`、`stat/fdist_test.go`、
`service/service_test.go`、`httpapi/handler_test.go`）：

1. **平方和恒等式**：SS零件 + SS测量人 + SS交互 + SS重复性 = SS总，相对误差 ≤ 1e-9
   （预置平衡算例、手算 2×2×2 算例、50 组随机非平凡数据）。
2. **预置 10×3×3 平衡算例**：数据按可手算的加性效应构造，手算平方和
   SS零件=2970、SS测量人=60、SS交互=0、SS重复=240、SS总=3270，写进回归测试。
3. **平移不变**：全部读数加同一常数，结果不变（仅总均值平移）。
4. **正比例不变**：全部乘正数 c，各标准差变 c 倍，占比与 NDC 不变（公差同向缩放时
   %GRR/公差也不变），平方和按 c² 缩放。
5. **编号重排不变**：零件、测量人编号任意置换，以及单元内重复次序重排，结果不变。
6. **平衡数据两条入口一致**：完整单元筛选路径与直接平衡 ANOVA 结果逐量相等。
7. **非平衡场景**：缺测、部分零件只被部分人测、单元重复次数不等，均能算且报告
   剔除/缺失明细。
8. **交互合并**：p>0.25 并入误差；R=1 时以交互为误差；显著交互保留。
9. **负方差分量**：负估计为 0 且标注。
10. **版本/定稿/返测/过期/冲突/重启持久化**：见服务层与 HTTP 层测试。
11. **输入校验定位字段**：非有限值、非正公差、水平数不足、重复 (part,operator,trial)。

F 分布 p 值用独立的数值积分（自适应 Simpson）交叉验证到 ~1e-15，并用若干 F 临界值
（上尾 0.05）与 F(1,1)、F(2,2) 闭式解核对。

---

## 7. 目录结构

```
cmd/server     程序入口（Gin + bbolt，数据落挂载卷）
stat           F 分布 p 值（自实现）、平衡 ANOVA、完整单元筛选
preset         预置 10×3×3 平衡算例与手算平方和
domain         持久化实体
store          bbolt 存储（事务、唯一槽位、版本/结果索引）
service        业务编排、校验、指纹、定稿、返测、结果过期
httpapi        Gin 路由、错误映射、非有限数字段扫描
```
