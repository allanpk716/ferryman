// 票05 · agent.inject 播种消息构造——零依赖版宿主 UserMessage 同形。
// 协议事实（dsh 调研克隆,只读）钉点：
//   - UserMessage 形状：MessageBase {id;content;source} + role:'user'
//     （packages/llm/llm/src/message.ts:150-154）;id=稳定身份（randomUUID,
//     createMessage 同语义 message.ts:222-230）;content=ContentBlock 数组,
//     文本块 {type:'text',text}（types.ts ContentBlock,官方桥构造先例
//     hooks-claude-code/src/index.ts:197-200）。
//   - source：merge-extensible——每个生产者声明自家 kind,消费方对未知 kind
//     落空处理（message.ts:110-118「user messages carry any producer's kind,
//     and consumers fall through unknown kinds」）;官方桥先例＝单一
//     CONTEXT_SOURCE {kind:'hooks-claude-code'}（index.ts:92-93,declare-
//     module 声明位 :18-21）。本插件同款单一 kind。
//   - inject 语义：排队赶下一个 pre-step、不唤醒 driver（runtime-types.ts:241
//     注文）;官方桥 SessionStart→inject 用法先例 index.ts:207-214。
//   - createMessage 会 deepFreeze（message.ts:222-230）——本构造浅冻三层
//     （外层/content 块/source）同防御语义;宿主不要求冻结,不臆造必填。

import { randomUUID } from "node:crypto";

/** 本插件自报家门的 producer kind（官方桥 CONTEXT_SOURCE 同位） */
export const SOURCE_KIND = "ferryman-dsh";

export interface TextBlock {
  type: "text";
  text: string;
}

/** 宿主 UserMessage 的结构化子集（零依赖：不 import 宿主类型,type-only 也免） */
export interface UserMessageLike {
  id: string;
  role: "user";
  content: TextBlock[];
  source: { kind: string };
}

/**
 * 构造注入用 user 消息（交接播种/observe 警告上下文共用）。
 * 每次调用 fresh 身份（createUserMessage randomUUID 同语义）。
 */
export function injectedMessage(text: string): UserMessageLike {
  return Object.freeze({
    id: randomUUID(),
    role: "user" as const,
    content: [Object.freeze({ type: "text" as const, text })],
    source: Object.freeze({ kind: SOURCE_KIND }),
  });
}
