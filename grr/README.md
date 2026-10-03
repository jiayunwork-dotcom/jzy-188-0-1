# grrd — Gage R&R (MSA) 测量系统分析后端

面向量具周期性测量系统分析的后端服务：平板录入或批量导入测量值，服务端按
**方差分析法（ANOVA）**计算零件、测量人、交互、重复性各项，以及 %GRR 和
可区分类别数（NDC）。Go 1.23 + Gin，数据存在挂载卷上的嵌入式 SQLite 文件里
（纯 Go 驱动，无 CGO），重启后研究、版本、原始读数和历次结果全部保留。

## 它解决的问题

- **数据不齐也能算。** 漏测、某零件被部分测量人测过、各单元重复次数不同，
  都有明确定义的算法，结果里写清楚这次实际用了哪些数据、剔掉了什么。
- **版本与原始记录不可覆盖。** 研究分 draft / finalized；定稿冻结一切写入。
  返测基于定稿版克隆出一个新版本，旧版本的数据和结果原样留存。
- **结果绑定数据版本。** 每个结果记录它依据的读数哈希（`data_hash`）；草稿
  数据一改，旧结果标记 `stale=true` 而不是被删除或覆盖，审核时可逐条追溯。
- **并发不静默覆盖。** 每条读数带 `row_version`，两个平板同改一条时后到的一
  方收到 `409 row_version_conflict`。
- **输入错误指到字段。** 非有限测量值、非正公差、零件或测量人少于 2、同一
  零件/人/次重复录入，都返回具体字段路径。

## 计算方法

三因素交叉设计：**零件 × 测量人 × 重复**（part × operator × trial）。

### 平衡数据

10×3×3 这类完整数据走经典的“有重复双因素方差分析”闭式公式：

| 来源 | 自由度 |
|---|---|
| 零件 part | a−1 |
| 测量人 operator | b−1 |
| 交互 part×operator | (a−1)(b−1) |
| 重复性 repeat | ab(n−1) |
| 合计 | abn−1 |

### 非平衡数据（默认）

默认 `method = "henderson"`：基于 **Henderson 方法 I（顺序平方和）** 的通用
引擎。依次拟合嵌套模型

```
M0 = 总均值
M1 = M0 + 零件效应
M2 = M1 + 测量人效应
M3 = M2 + 零件×测量人单元效应
```

令 P_k = H(M_k) − H(M_{k−1})（正交投影之差），各项平方和就是 y′P_k y，
**对任何缺测模式都严格分区总平方和**（数值误差 ≤ 1e-9）。期望均方不靠
“平衡”假设推导，而是直接计算迹

```
E(MS_k) = Σ_c  tr(P_k Z_c Z_c′)/df_k · σ²_c  +  σ²_e
```

其中 Z_c 是零件、测量人、单元的原始示性矩阵。投影矩阵用带列选主的
Gram-Schmidt（rank-revealing QR）构造，自动处理空单元、重复次数不同、
设计不连通等情况。主效应的 F 检验分母用 EMS 匹配的 Satterthwaite 合成
（枚举均方非负组合）；平衡时合成结果就是教科书的精确分母 df，非平衡且无
精确组合时结果标注为近似。交互项始终对重复误差精确检验。

**为什么选它做默认，而不是“直接剔零件”：** 剔零件（balanced_drop）实现简
单、结果是熟悉的经典 ANOVA，但会丢掉整台零件的全部读数，缺测稍多就只剩
两三个零件，零件变差和 NDC 严重失真。Henderson I 用上每一条已测数据，对
任意缺失模式都有定义，且在数据恰好平衡时与经典 ANOVA **完全一致**（本仓库
有 50 组随机平衡数据的逐行对比回归测试）。它的代价是顺序平方和对因素次序
有定义上的依赖——本设计固定“零件先于测量人”，这与 AIAG Gage R&R 的关注
点一致（测量人变差是要评估的测量系统误差，零件变差是背景）。

### 交互并入与负方差分量

- 交互项 **p > 0.25** 时，交互空间并入重复性，模型重算；结果的
  `data_usage.interaction_pooled` 与 `pool_reason` 说明这一点。
- 若缺失模式导致交互无法对残差检验（例如每单元只有一次测量、残差 df=0），
  同样并入并注明原因。
- 方差分量解为负值时，分量**原始估计**保留在 `raw_estimate`，报告估计值取
  0 且 `clipped_to_zero=true`。主效应方程使用未截断的原始交互分量求解，避
  免截断误差向上游传播。

### 报告量

- 重复性 EV、再现性 AV（本设计即测量人分量）、GRR = √(EV²+AV²)、
  零件变差 PV、总变差 TV 的标准差；
- 各方差分量及占总变差百分比；
- `pct_grr_of_total` = σ_GRR / σ_TV（相对总变差 %GRR）；
- `pct_grr_of_tolerance` = 6σ_GRR / 公差 ×100（相对公差 %GRR）；
- `ndc = floor(1.41 · PV / GRR)`。

### 数学自检

每个结果都带 `ss_partition_check`：各来源 SS 之和与总 SS 的相对误差，要求
≤ 1e-9。另在回归测试中验证：

1. 所有 SS 相加 = 总 SS（平衡与大量随机缺测模式）；
2. 读数全部加常数，结果不变；
3. 读数全部乘正数 c（公差同比缩放），所有标准差变 c 倍，占比、NDC 不变；
4. 零件/测量人编号任意重排，结果不变；
5. 平衡数据经通用引擎与经典闭式 ANOVA，SS/df/MS/分量逐项一致。

F 分布 p 值由本仓库自行实现（正则化不完全 Beta 的 Lentz 连分式），未引入
任何统计库。

## HTTP 接口

基址 `/api/v1`，全部 JSON。

| 方法 | 路径 | 说明 |
|---|---|---|
| GET | `/health` | 健康检查 |
| POST | `/studies` | 建研究（`gage_id`、`characteristic`、`tolerance`），同时产生 draft v1 |
| GET | `/studies` | 列表（`limit`/`offset`） |
| GET/PUT | `/studies/:id` | 查看/改表头 |
| GET | `/studies/:id/versions` | 版本列表 |
| GET | `/studies/:id/draft` | 当前草稿版 |
| POST | `/studies/:id/versions` | 基于定稿版开新版本（正文可带 `based_on_id`，默认最新定稿版） |
| GET | `/versions/:id` | 版本详情 |
| POST | `/versions/:id/finalize` | 定稿（先校验数据） |
| GET/POST | `/versions/:id/measurements` | 列表 / 逐条录入 |
| POST | `/versions/:id/measurements/import` | 整批导入（任一行非法整批拒绝） |
| PUT | `/versions/:id/measurements/:mid` | 改读数（乐观锁） |
| DELETE | `/versions/:id/measurements/:mid` | 删读数 |
| POST | `/versions/:id/compute` | 计算并保存结果（`method`: `henderson` 默认 / `balanced_drop`） |
| GET | `/versions/:id/results` | 该版本全部历史结果（含 stale） |

### 乐观并发

读数响应里有 `row_version`。修改时必须提供：

```json
{ "part_id": "P1", "operator_id": "O1", "trial": 1, "value": 12.34,
  "expected_row_version": 3 }
```

（也可用 `If-Match: 3` 请求头。）行已被他人改过则返回：

```
409 {"error":{"code":"row_version_conflict", ...}}
```

### 定稿与写入保护

定稿版上任何 measurements 写入返回
`409 {"error":{"code":"version_finalized"}}`。定稿版仍允许 `compute`——
那是只读计算，不改任何存储。要返测就开新版本：草稿基于定稿版克隆全部读
数，旧版本与旧结果不动。

### 错误格式

字段级校验错误（HTTP 422）：

```json
{"error":{"message":"validation failed","fields":[
  {"field":"tolerance","message":"tolerance must be a positive finite number"}
]}}
```

批量导入的错误定位到 `readings[7].value` 这样的具体路径。

## 运行

### Docker（两阶段构建）

```bash
docker build -t grrd:latest .
docker run -d -p 8080:8080 -v /var/lib/grr:/data grrd:latest
```

数据文件 `/data/grr.db`（WAL 模式，另有 `-wal`/`-shm` 伴生文件）。挂载卷
要保证这几个文件一起持久化。

### 本地

```bash
go test ./...                       # 全部回归测试
go run ./cmd/grrd                   # 默认 GRR_DATA_DIR=/data
GRR_DATA_DIR=./data GRR_ADDR=:8080 go run ./cmd/grrd
```

## 快速示例

```bash
curl -s localhost:8080/api/v1/studies -d '{
  "gage_id":"MIC-007","characteristic":"外径","tolerance":0.05}'
# -> study.id, version.id

curl -s localhost:8080/api/v1/versions/$VID/measurements/import -d '{
  "readings":[{"part_id":"P1","operator_id":"A","trial":1,"value":9.98}, ...]}'

curl -s localhost:8080/api/v1/versions/$VID/compute -d '{}'
```
