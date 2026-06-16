---
name: example
description: 示例技能，用于验证 .claude/skills/<name>/SKILL.md 发现机制
when-to-use: 当用户输入 /example 时调用，测试技能发现是否正确工作
user-invocable: true
context: inline
allowed-tools: read_file, write_file, run_shell
---

# Example Skill

这个示例技能用于验证 `.claude/skills/<name>/SKILL.md` 的发现机制是否生效。
