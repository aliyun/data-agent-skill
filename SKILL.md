---
name: alibabacloud-data-agent-skill
description: |
  通过命令行调用阿里云瑶池 Data Agent for Analytics，帮助用户对企业数据库进行自然语言驱动的数据分析。
  Data Agent for Analytics 是阿里云瑶池数据库团队推出的面向企业用户的数据分析智能体，可根据自然语言描述自动完成需求分析、数据理解、分析洞察及报告生成。
  本工具支持：发现已托管在 DMS 的数据资源（实例/库/表）、发起问数或深度分析会话、实时跟踪执行进度、获取分析结论及生成的报告文件。
  当用户需要查询数据库、分析数据趋势、生成数据报告、用自然语言问数，或提到"Data Agent"、"数据分析"、"数据库查询"、"SQL分析"、"数据洞察"时使用本 Skill。
compatibility: |
  需要有效的阿里云凭证（默认凭证链或 API_KEY）；
  需要安装 requirements.txt 中的依赖；
  数据源需已托管在阿里云瑶池数据库或 DMS。
domain: AIOps
---
metadata:
  author: DataAgent 研发
  version: "1.7.2"
---

# 更新日志
- **v1.7.2**: 使用阿里云默认凭证链替代显式 AK/SK、添加 User-Agent 头、修复 RAM 权限通配符问题
- **v1.7.1**: 修复 CLI `ls` 命令 API 响应解析问题（支持大小写字段名）、优化 SKILL 文档结构、ANALYSIS 模式规范独立文档
- **v1.7.0**: API_KEY 认证支持、原生异步执行模式、会话隔离、增强的attach模式、优化的日志输出

---

---

# 安装


## 配置凭证

本 Skill 使用阿里云默认凭证链（推荐）或 API_KEY 认证。

### 方式一：默认凭证链（推荐）

Skill 使用阿里云 SDK 的默认凭证链自动获取凭证，支持环境变量、配置文件、实例角色等方式。

详见 [阿里云凭证链文档](https://help.aliyun.com/document_detail/378659.html)

### 方式二：API_KEY 认证（仅文件分析）

```bash
export DATA_AGENT_API_KEY=your-api-key
export DATA_AGENT_REGION=cn-hangzhou
```

获取 API_KEY：[Data Agent 控制台](https://agent.dms.aliyun.com/cn-hangzhou/api-key)

### 权限要求

RAM 用户需具有 `AliyunDMSFullAccess` 或 `AliyunDMSDataAgentFullAccess` 权限。
详细权限说明见 [RAM-POLICIES.md](references/RAM-POLICIES.md)

## 调试功能

```bash
DATA_AGENT_DEBUG_API=1 python3 scripts/data_agent_cli.py file example.csv -q "分析"
```

## 💡 使用建议

- 使用内置体验库 `internal_data_employees`（DataAgent 内置的测试数据库，包含员工、部门、薪资等数据）进行首次体验
- 或使用本地文件 `assets/example_game_data.csv` 完成文件分析体验


# Data Agent CLI — 统一命令行数据分析工具

## 概述

`scripts/data_agent_cli.py` 帮助用户完成从**发现数据 → 发起分析 → 跟踪进度 → 获取结果**的完整流程。

### 核心概念

> **⚠️ 关键前提**：Data Agent 只能分析**已导入到 Data Agent Data Center** 的数据库。
>
> - **Data Center**：Data Agent 的数据中心，只有这里的数据库才能被分析
> - **DMS**：阿里云数据管理服务，存储着所有数据库的元数据
> - **关系**：DMS 中注册的数据库 ≠ Data Center 中的数据库
>
> **使用流程**：
> 1. 先用 `ls` 查看 Data Center 中是否有目标数据库
> 2. 如果**没有**，先用 `dms` 子命令搜索数据库信息，再用 `import` 子命令导入
> 3. 导入成功后，才能使用 `db` 子命令进行分析

---

## 分析模式

- **ASK_DATA**（默认）：同步执行，秒级响应，适合即问即答
- **ANALYSIS**：深度分析，耗时 5-40 分钟，需 spawn 子 Agent 异步执行或者使用--async-run参数

> 详见 [ANALYSIS_MODE.md](references/ANALYSIS_MODE.md)

---

## 会话复用

首次分析使用 `db`/`file` 创建会话，后续追问使用 `attach --session-id <ID>` 复用会话。

> 详见 [COMMANDS.md](references/COMMANDS.md) 和 [WORKFLOWS.md](references/WORKFLOWS.md)

---

## 快速开始

```bash
# 1. 查看可用数据库
python3 scripts/data_agent_cli.py ls

# 2. 问数分析（同步返回结果）
python3 scripts/data_agent_cli.py db \
    --dms-instance-id <ID> --dms-db-id <ID> \
    --instance-name <NAME> --db-name <DB> \
    --tables "employees,departments" -q "哪个部门平均工资最高"

# 3. 追问（复用会话）
python3 scripts/data_agent_cli.py attach --session-id <ID> -q "按月分解"
```

> 📖 完整工作流、命令参考和最佳实践见 [WORKFLOWS.md](references/WORKFLOWS.md) 和 [COMMANDS.md](references/COMMANDS.md)

---

## 项目结构

```
                          # Skill 根目录
├── SKILL.md              # 本文档
├── scripts/              # 源代码
│   ├── data_agent/       # SDK 模块
│   ├── cli/              # CLI 模块
│   ├── data_agent_cli.py # CLI 入口
│   └── requirements.txt  # 依赖
├── sessions/             # 会话数据
└── references/           # 参考文档
```
