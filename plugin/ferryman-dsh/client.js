// client.js — 票08 · F1 双面包的浏览器半面（零依赖手写,不引构建链）。
//
// 协议事实（dsh 调研克隆,只读）钉点：
//   - 分发：package.json 声明 dsh.client{platform:'web'} ＋ exports["./client"]
//     （本文件）→ 宿主扫描器编入浏览器 roster（client/modules/src/index.ts:823-858
//     resolveMeta;:194-205 clientExportOf;:194 parseDshClient 在
//     modules/src/client/manifest.ts:161-181）。本文件原文被拼进 combo 脚本
//     （:400-405 buildComboScript 纯拼接）,加载时自调
//     window.__ModuleLoader__.load({id, factory})——懒 CJS 模型,执行只注册工厂,
//     求值发生在首次 import（manifest.ts:16-21）。id=包名（roster 行 id）。
//   - factory(require) 收模块表 require:react 走平台共享模块
//     （client/web/src/platform.ts:8-14 PLATFORM_MODULES）;返回
//     {name, inject, apply}（vendor/cordis/src/registry.ts:222-228 加载器收形）。
//   - 调用面：宿主插件经 typert SRC 路暴露 ferrymanBlocked/{list,resend,
//     newSession}（见 src/index.ts buildBlockedService 钉点）;浏览器侧走应用
//     自己的网关 RPC（POST /api/ferrymanBlocked/<method>,client-request 信封,
//     同源 cookie 鉴权）。10-06 真机事故三:typert 客户端面（$mount +
//     remote.<ns> 命名空间）在装机宿主全 bundle 零先例、命名空间服务经
//     cordis inject 执法不可达（声明进 inject 又成死锁——服务恰由 $mount 建）,
//     裸 RPC 为实测可用面;方法/参数名与宿主端点的一致性由
//     test/blocked.test.ts 跨面钉死。
//   - 卡面位置：conversation.composer.dock 列表位（kind:'list', scope:'session',
//     ui-conversation/src/client/contract/slots.ts:195）——QueueDock 同位
//     （spike F1 定路;注册先例 ui-goal/src/client/index.ts:92-144,按会话
//     inject 回调发业务面）。被拦事件不是会话日志事件,无对话流内嵌节点可挂,
//     输入坞卡面是平台上离「对话区选择框」最近的可交互落点（mock 信息结构
//     原样保留,配色走平台 design token）。
//   - 已知取舍（ADR-0023）：宿主重启丢缓存 → 卡片数据为空,拦截反馈回落票10
//     logger 文案;DSH 前端内部面（slot 名/roster 机制）随宿主升级可能漂移,
//     失灵只降级回「消息消失+日志一行」,闸门本体在 daemon 侧不受影响。
//
// 文案为中文硬编码（本插件用户面即中文;平台 locale 面留待需要时再接）。
;(function (global) {
  'use strict';

  var target = global.__ModuleLoader__;
  if (!target || typeof target.load !== 'function') {
    // 壳的 HTML 引导先于一切应用包装好 __ModuleLoader__（manifest.ts:348-358）
    // ——缺它说明不在 dsh web 壳里,响亮失败而非静默错装。
    throw new Error('ferryman-dsh/client: window.__ModuleLoader__ is unavailable');
  }
  target.load({ id: 'ferryman-dsh', factory: factory });

  var NAMESPACE = 'ferrymanBlocked';
  // 真槽名 = conversation.composer.dock（dsh 源码 ui-conversation/src/client/apply.ts:432
  // 声明 { kind:'list', scope:'session' },InputBar.tsx:501 渲染;ui-chat 的 stats 药丸同位先例）。
  // 10-06 真机事故:此前误用 spike 猜名 conversation.input.dock——app 无此槽,注册即沉海,
  // 模块/服务/RPC 全通但卡面永不渲染。
  var DOCK_SLOT = 'conversation.composer.dock';
  var POLL_MS = 4000;
  var PREVIEW_CAP = 120; // 原话预览码点上限（mock 说明:超长截断）


  function factory(require) {
    var React = require('react');
    var h = React.createElement;

    // ---- 样式（平台 design token 优先,fallback 为 mock 占位色） ----

    var S = {
      dock: { display: 'flex', flexDirection: 'column', gap: '8px' },
      card: {
        background: 'var(--dsw-alias-bg-layer-2, #1d2030)',
        border: '1px solid var(--dsw-alias-border-l2, #3a4160)',
        borderRadius: '14px',
        padding: '13px 14px',
        // 10-06 真机反馈:窄屏/错误态下卡片高过可视区、底端被输入坞区盖住——
        // 自限高+内部滚动,任何屏都完整可读
        maxHeight: '45vh',
        overflowY: 'auto',
        position: 'relative',
        zIndex: 3,
      },
      head: {
        display: 'flex', alignItems: 'center', gap: '8px',
        fontSize: '13.5px', fontWeight: 600,
        color: 'var(--dsw-alias-label-primary, #e8eaf2)',
        marginBottom: '7px',
      },
      dot: {
        width: '8px', height: '8px', borderRadius: '50%', flex: 'none',
        background: 'var(--dsw-alias-label-warning, #e0b34a)',
      },
      reason: {
        fontSize: '12.5px', lineHeight: 1.5, marginBottom: '9px',
        color: 'var(--dsw-alias-label-secondary, #9aa0b5)',
        display: '-webkit-box', WebkitLineClamp: 3, WebkitBoxOrient: 'vertical', overflow: 'hidden',
      },
      quote: {
        borderLeft: '3px solid var(--dsw-alias-border-l2, #3a4160)',
        background: 'var(--dsw-alias-bg-layer-1, rgba(255,255,255,0.03))',
        padding: '7px 10px', borderRadius: '0 8px 8px 0',
        fontSize: '13px', marginBottom: '11px', wordBreak: 'break-all',
        color: 'var(--dsw-alias-label-primary, #e8eaf2)',
        display: '-webkit-box', WebkitLineClamp: 4, WebkitBoxOrient: 'vertical', overflow: 'hidden',
      },
      quoteLabel: {
        display: 'block', fontSize: '11px', marginBottom: '3px',
        color: 'var(--dsw-alias-label-secondary, #9aa0b5)',
      },
      btns: { display: 'flex', flexDirection: 'column', gap: '8px' },
      btnPrimary: {
        display: 'block', width: '100%', padding: '11px 0',
        borderRadius: '10px', fontSize: '14.5px', fontWeight: 600,
        textAlign: 'center', cursor: 'pointer', border: 'none',
        background: 'var(--dsw-alias-brand-primary, #5b8cff)',
        color: 'var(--dsw-alias-label-primary-foreground, #ffffff)',
      },
      btnGhost: {
        display: 'block', width: '100%', padding: '11px 0',
        borderRadius: '10px', fontSize: '14.5px', fontWeight: 600,
        textAlign: 'center', cursor: 'pointer',
        background: 'transparent',
        border: '1px solid var(--dsw-alias-border-l2, #3a4160)',
        color: 'var(--dsw-alias-label-primary, #e8eaf2)',
      },
      sub: { display: 'block', fontSize: '11px', fontWeight: 400, opacity: 0.72, marginTop: '2px' },
      foot: {
        fontSize: '11px', marginTop: '9px', textAlign: 'center',
        color: 'var(--dsw-alias-label-secondary, #9aa0b5)',
      },
      err: {
        fontSize: '12px', marginTop: '8px',
        color: 'var(--dsw-alias-label-error, #e5484d)',
      },
      doneRow: {
        display: 'flex', alignItems: 'center', gap: '8px',
        fontSize: '12.5px', wordBreak: 'break-all',
        padding: '9px 14px', borderRadius: '14px', opacity: 0.85,
        background: 'var(--dsw-alias-bg-layer-2, #1d2030)',
        border: '1px solid var(--dsw-alias-border-l2, #3a4160)',
        color: 'var(--dsw-alias-label-secondary, #9aa0b5)',
      },
      tick: {
        width: '16px', height: '16px', borderRadius: '50%', flex: 'none',
        display: 'flex', alignItems: 'center', justifyContent: 'center',
        fontSize: '10px', color: '#ffffff',
        background: 'var(--dsw-alias-code-diff-added, #3f9e6e)',
      },
    };

    /** 码点截断预览（增补平面字符按 1 计） */
    function preview(text) {
      var cps = Array.from(text);
      return cps.length <= PREVIEW_CAP ? text : cps.slice(0, PREVIEW_CAP).join('') + '…';
    }

    // ---- 业务面（按会话经 dock inject 回调发出） ----

    // 调用通道 = 应用自己的网关 RPC（POST /api/<ns>/<method>,client-request 信封,
    // 同源 cookie 鉴权）。10-06 真机事故三:typert 客户端面（$mount + remote.<ns>）
    // 在装机宿主全 bundle 无先例、命名空间服务经 inject 执法不可达——裸 RPC 为
    // 实测可用面（真机页面 fetch 实证往返真卡数据）。
    var rpcSeq = 0;
    function rpc(method, args) {
      return fetch('/api/' + NAMESPACE + '/' + method, {
        method: 'POST',
        headers: { 'content-type': 'application/json' },
        body: JSON.stringify({ type: 'client-request', rpcId: 'ferryman-dsh-' + (++rpcSeq), method: NAMESPACE + '/' + method, payload: { args: args } }),
      }).then(function (r) { return r.json(); }).then(function (env) {
        var res = env && env.result;
        if (res && res.ok) return res.value;
        return { ok: false, error: (res && res.error && res.error.message) || '网关拒绝' };
      }).catch(function (e) {
        return { ok: false, error: (e && e.message) ? e.message : String(e) };
      });
    }

    function makeFace(sessionId) {
      return {
        sessionId: sessionId,
        list: function () { return rpc('list', { sessionId: sessionId }); },
        resend: function (id) { return rpc('resend', { id: id }); },
        newSession: function (id) { return rpc('newSession', { id: id }); },
      };
    }

    // ---- 卡片组件（函数组件;props.ferrymanBlocked = 注入面） ----

    function CollapsedCard(card) {
      var action = card.doneAction === 'new-session' ? '已按「新会话」继续' : '已按「强续」重发';
      return h('div', { key: card.id, style: S.doneRow, 'data-ferryman-blocked-done': card.id }, [
        h('span', { style: S.tick, 'aria-hidden': 'true' }, '✓'),
        action + '——原话:' + preview(card.prompt),
      ]);
    }

    function PendingCard(card, face, busy, act) {
      var quote = card.prompt
        ? h('div', { style: S.quote }, [
          h('span', { style: S.quoteLabel }, '你刚发的原话(没有丢)'),
          preview(card.prompt),
        ])
        : null;
      return h('div', { key: card.id, style: S.card, 'data-ferryman-blocked': card.id }, [
        h('div', { style: S.head }, [
          h('span', { style: S.dot, 'aria-hidden': 'true' }),
          '这条消息被 Ferryman 闸门拦下',
        ]),
        h('div', { style: S.reason }, card.reason),
        quote,
        h('div', { style: S.btns }, [
          h('button', {
            type: 'button',
            style: S.btnPrimary,
            disabled: busy ? true : undefined,
            onClick: function () { return act('resend', card); },
          }, [
            '强续重发',
            h('span', { style: S.sub }, '留在本会话 · 以「强续」开头重发这句话(接受全量重付)'),
          ]),
          h('button', {
            type: 'button',
            style: S.btnGhost,
            disabled: busy ? true : undefined,
            onClick: function () { return act('newSession', card); },
          }, [
            '新会话继续',
            h('span', { style: S.sub }, '一键新建 · 开场自动收到进度交接 + 这句原话'),
          ]),
        ]),
        h('div', { style: S.foot }, '也可以无视此卡,手动输入任意「强续 …」或新建会话'),
      ]);
    }

    function BlockedDock(props) {
      var face = props.ferrymanBlocked;
      var statePair = React.useState({ cards: [], ready: false });
      var cards = statePair[0];
      var setCards = statePair[1];
      var busyPair = React.useState('');
      var busy = busyPair[0];
      var setBusy = busyPair[1];
      var errPair = React.useState('');
      var err = errPair[0];
      var setErr = errPair[1];

      function refresh() {
        return face.list().then(function (r) {
          if (r && r.ok && Array.isArray(r.cards)) {
            setCards({ cards: r.cards, ready: true });
          }
        }).catch(function () { /* 轮询失败静默,下轮再试 */ });
      }

      React.useEffect(function () {
        var alive = true;
        var tick = function () {
          face.list().then(function (r) {
            if (alive && r && r.ok && Array.isArray(r.cards)) {
              setCards({ cards: r.cards, ready: true });
            }
          }).catch(function () { /* 静默 */ });
        };
        tick();
        var timer = global.setInterval(tick, POLL_MS);
        return function () {
          alive = false;
          global.clearInterval(timer);
        };
      }, []);

      if (!cards.ready || cards.cards.length === 0) return null;

      function act(method, card) {
        setBusy(card.id);
        setErr('');
        return face[method](card.id).then(function (r) {
          if (r && r.ok) {
            // 动作落宿主 → 重拉为准（塌缩态走服务端真相,刷新页面也不回弹）
            return refresh().then(function () { return r; });
          }
          setErr((r && r.error) ? r.error : '动作失败,可重试');
          return r;
        }).catch(function (e) {
          setErr((e && e.message) ? e.message : String(e));
        }).then(function (r2) {
          setBusy('');
          return r2;
        });
      }

      var children = cards.cards.map(function (card) {
        return card.done
          ? CollapsedCard(card)
          : PendingCard(card, face, busy === card.id, act);
      });
      if (err) {
        children.push(h('div', { key: 'ferryman-err', style: S.err, role: 'alert' }, err));
      }
      return h('div', { style: S.dock, 'data-ferryman-blocked-dock': face.sessionId }, children);
    }

    // ---- 插件体 ----

    function apply(ctx) {
      var slots = ctx && ctx.slots;
      if (!slots || typeof slots.inject !== 'function' || typeof slots.register !== 'function') {
        // 非 web 壳或老宿主缺面:本半面自降级（宿主侧拦截/票10 文案不受影响）
        return;
      }

      function registerDock() {
        return slots.inject(DOCK_SLOT, function () {
          return slots.register({
            name: DOCK_SLOT,
            id: 'ferryman-blocked',
            order: 15, // TodoDock(0) / GoalBar(10) 之下、QueueDock(20) 之上
            inject: function (sessionId) {
              return { ferrymanBlocked: makeFace(sessionId) };
            },
          }, BlockedDock);
        });
      }

      if (typeof ctx.effect === 'function') {
        ctx.effect(function () {
          var stop = registerDock();
          return function () {
            if (typeof stop === 'function') stop();
          };
        }, 'ferryman-dsh client: blocked dock');
      } else {
        registerDock();
      }
    }

    return { name: 'ferryman-dsh', inject: ['slots'], apply: apply };
  }
})(typeof window !== 'undefined' ? window : globalThis);
