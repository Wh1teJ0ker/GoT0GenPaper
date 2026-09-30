# 配置说明

CLI 优先使用环境变量 `GOT0GENPAPER_CONFIG` 指定的 TOML 文件；否则使用当前目录的 `config.toml`，若不存在则使用系统用户配置目录下的 `GoT0GenPaper/config.toml`。首次配置可复制无密钥模板：

```bash
cp config.example.toml config.toml
chmod 600 config.toml
```

`config.toml` 含 API Key 时只保存在本机，并已加入 `.gitignore`。不要把它提交或粘贴到日志。密钥也可通过 `NEWAPI_API_KEY` 环境变量覆盖 active provider 的 TOML 值。

## 多供应商 TOML

每个供应商使用一个 `[[providers.providers]]` 表。`active_id` 决定日常生成、答案、批改和视觉模型所用配置；`model_by_type` 可分别路由题型。

```toml
[providers]
active_id = "glm-local"

[[providers.providers]]
id = "glm-local"
name = "Local GLM 5.2"
base_url = "http://127.0.0.1:3001"
api_key = ""
default_model = "glm-5.2"
judge_model = "glm-5.2"
vision_model = "glm-5.2"
reasoning_effort = "low"
max_tokens = 8192
enabled = true

[providers.providers.model_by_type]
choice = "glm-5.2"
fill_blank = "glm-5.2"
major = "glm-5.2"
```

`judge_model` 用于答案核验/文本主观批改，`vision_model` 用于扫描图批改，`default_model` 是通用回退模型。服务商需要支持 OpenAI 兼容 Chat Completions 接口；扫描批改要求 `vision_model` 支持图像输入。项目不依赖 Tesseract 或本地 OCR。

CLI 管理命令：

```bash
got0genpaper config list
got0genpaper config show
got0genpaper config use glm-local
got0genpaper config add --id deepseek --name DeepSeek --base-url https://api.deepseek.com --model deepseek-chat
got0genpaper config remove deepseek
got0genpaper test-provider --provider glm-local
```

不要在命令行 `--api-key` 参数中放真实密钥，因为 shell 历史或进程信息可能记录命令。建议编辑权限为 `0600` 的 TOML，或对当前 active provider 设置 `NEWAPI_API_KEY`。

## 环境变量

| 变量 | 覆盖内容 |
| --- | --- |
| `GOT0GENPAPER_CONFIG` | TOML 配置路径 |
| `NEWAPI_API_KEY` | 当前 active provider 的 API Key |
| `NEWAPI_BASE_URL` | 当前供应商 URL |
| `NEWAPI_MODEL_DEFAULT` | 默认模型 |
| `NEWAPI_MODEL_CHOICE` | 选择题模型 |
| `NEWAPI_MODEL_FILL_BLANK` | 填空题模型 |
| `NEWAPI_MODEL_MAJOR` | 解答题模型 |
| `NEWAPI_MODEL_JUDGE` | 答案核验和主观批改模型 |
| `NEWAPI_MODEL_VISION` | 扫描图片批改模型 |

环境变量只影响当前进程，不会自动写回 TOML。所有相对路径均相对于配置文件所在目录解析。

## LaTeX 模板

`paths.templates` 指向完整模板目录，默认为 `./templates`。程序加载时校验必需模板；自定义目录需复制完整的 `.tmpl` 集合，并保留 Go `text/template` 的 `define` 名称。模板分隔符为 `{{%` 和 `%}}`，避免与 LaTeX 花括号冲突。

| 文件 | 内容 |
| --- | --- |
| `document_preamble.tmpl` | 导言区和页脚 |
| `exam_document.tmpl` | 试卷封面与正文 |
| `answers_document.tmpl`、`answer_block.tmpl` | 答案和评分标准 |
| `choice_question.tmpl`、`major_question.tmpl` | 选择题与解答题 |
| `answer_sheet.tmpl` 及同名前缀片段 | 答题卡版式与定位标记 |
| `question_*.tmpl` | 提供给生成器的题型骨架 |

配置变更后不需要重新编译 CLI；路径和供应商在每次启动时加载。旧版 `config.json` 仅作为一次性迁移输入，新配置保存为 TOML。
