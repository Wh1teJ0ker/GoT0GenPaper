# 配置说明

CLI 优先使用 `GOT0GENPAPER_CONFIG` 指定的 TOML 文件；否则依次使用当前目录的 `config.toml`，或用户配置目录下的 `GoT0GenPaper/config.toml`。

```bash
cp config.example.toml config.toml
chmod 600 config.toml
```

配置包含 API Key 时只保存在本机，不要提交到仓库或粘贴进日志。

## 当前供应商

当前运行配置只保留本地 `http://localhost:8792/` 的 `glm-5.3-flash`。`model_by_type` 按题型指定模型；`default_model` 用于没有专属映射时的生成。可选的 `fallback_model` 在主生成模型请求失败时作为备用模型。

```toml
[providers]
active_id = "local-glm-5.3-flash"

[[providers.providers]]
id = "local-glm-5.3-flash"
name = "Local GLM 5.3 Flash (8792)"
base_url = "http://localhost:8792/"
api_key = ""
default_model = "glm-5.3-flash"
reasoning_effort = "low"
max_tokens = 8192
enabled = true

[providers.providers.model_by_type]
choice = "glm-5.3-flash"
fill_blank = "glm-5.3-flash"
major = "glm-5.3-flash"
```

旧配置中的 `judge_model` 会在加载时迁移为 `fallback_model`；旧环境变量 `NEWAPI_MODEL_JUDGE` 也仍作为兼容别名。新配置请使用 `fallback_model` / `NEWAPI_MODEL_FALLBACK`。

```bash
got0genpaper config list
got0genpaper config show
got0genpaper config use local-glm-5.3-flash
got0genpaper test-provider --provider local-glm-5.3-flash
```

避免把密钥写进 `--api-key` 参数，以免被 shell 历史或进程列表记录。编辑权限为 `0600` 的 TOML，或使用 `NEWAPI_API_KEY` 临时覆盖 active provider。

## 环境变量

| 变量 | 用途 |
| --- | --- |
| `GOT0GENPAPER_CONFIG` | TOML 配置路径 |
| `NEWAPI_API_KEY` | active provider API Key |
| `NEWAPI_BASE_URL` | active provider Base URL |
| `NEWAPI_MODEL_DEFAULT` | 默认生成模型 |
| `NEWAPI_MODEL_CHOICE` | 选择题模型 |
| `NEWAPI_MODEL_FILL_BLANK` | 填空题模型 |
| `NEWAPI_MODEL_MAJOR` | 解答题模型 |
| `NEWAPI_MODEL_FALLBACK` | 生成备用模型 |

相对路径以 TOML 文件所在目录解析。环境变量仅覆盖当前进程，不会写回配置文件。

## LaTeX 模板

`paths.templates` 指向模板目录，默认是 `./templates`。程序启动时校验试卷必需模板；自定义目录需要包含完整 `.tmpl` 集合。模板分隔符为 `{{%` 和 `%}}`，避免与 LaTeX 花括号冲突。

主要模板包括 `document_preamble.tmpl`、`exam_document.tmpl`、题型模板和生成器使用的 `question_*.tmpl`。当前不加载答案册或答题卡模板。

PDF 编译使用 PATH 中的 `tectonic` 或 `xelatex`。当前没有把 LaTeX 引擎嵌入 Go 二进制。
