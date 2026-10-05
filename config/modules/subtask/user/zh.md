{{ Task|safe }}
{% if enable_skill_registry %}

## 已注册 Skill

主线程为这个工作包注册了以下 Skill。任务匹配时，先按其 `location` 读取 `SKILL.md`：

```json
{{ SKILL_REGISTRY_JSON|safe }}
```
{% endif %}
