"use client";
import { useState } from "react";
import {
  Check,
  ChevronDown,
  FileCode2,
  GitBranch,
  Laptop,
  LockKeyhole,
  Smartphone,
} from "lucide-react";
import paths from "../../../internal/brandicons/paths.json";
import { useT } from "./locale";

const sessions = [
  {
    brand: "openai",
    name: "Codex",
    title: "搜索栏的键盘导航",
    task: "让上下方向键可以切换搜索结果。",
    file: "src/search.tsx",
    result: "方向键现在可以移动选中项，按回车打开结果。",
    follow: "再测一下没有结果的情况。",
  },
  {
    brand: "claude",
    name: "Claude Code",
    title: "复核登录流程",
    task: "检查邮箱验证码过期后的提示。",
    file: "src/sign-in.tsx",
    result: "验证码过期时，登录页会显示重新发送入口。",
    follow: "把这个场景加进测试。",
  },
  {
    brand: "pi",
    name: "Pi",
    title: "整理发布清单",
    task: "整理这次发布的安装步骤。",
    file: "docs/install.md",
    result: "安装文档已经按平台列出下载和启动步骤。",
    follow: "补上 Linux 的安装示例。",
  },
] as const;

export function WorkspaceDemo() {
  const t = useT();
  const [index, select] = useState(0);
  const current = sessions[index];
  return (
    <figure className="console-case">
      <figcaption className="console-plate">
        <strong>
          <Laptop size={21} />
          {t("工作台示例")}
        </strong>
        <span>
          <i className="console-led" />
          MacBook Pro
        </span>
      </figcaption>
      <div className="console-grid">
        <div className="console-screen" aria-live="polite">
          <div className="screen-bar">
            <span>~/atlas</span>
            <span>
              <GitBranch size={13} />
              main
            </span>
          </div>
          <p className="screen-task">{t(current.task)}</p>
          <strong className="screen-title">{t(current.title)}</strong>
          <p className="screen-result">{t(current.result)}</p>
          <details key={current.file} className="console-change">
            <summary>
              <FileCode2 size={15} />
              <code>{current.file}</code>
              <ChevronDown size={14} />
            </summary>
            <div>
              <Check size={14} />
              {t("已更新文件")}
            </div>
          </details>
        </div>
        <div className="console-controls">
          <div className="console-keys" role="group" aria-label={t("项目会话")}>
            {sessions.map((s, i) => (
              <button
                key={s.brand}
                type="button"
                aria-pressed={index === i}
                onClick={() => select(i)}
              >
                <svg viewBox="0 0 24 24" aria-hidden="true">
                  <path fill="currentColor" d={paths[s.brand]} />
                </svg>
                <span>{s.name}</span>
                <i aria-hidden="true" />
              </button>
            ))}
          </div>
          <div className="console-reply" aria-live="polite">
            <div>
              <Smartphone size={15} />
              {t("来自手机")}
              <time>14:32</time>
            </div>
            <p>{t(current.follow)}</p>
          </div>
          <div className="console-route">
            <LockKeyhole size={13} />
            {t("端到端加密")}
            <span aria-hidden="true" />
          </div>
        </div>
      </div>
    </figure>
  );
}
