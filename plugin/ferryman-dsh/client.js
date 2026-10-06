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
//   - Remote 面：宿主插件经 typert SRC 路暴露 ferrymanBlocked/{list,resend,
//     newSession}（见 src/index.ts buildBlockedService 钉点）;浏览器侧
//     ctx.remote.$mount(手写描述符)（api/gateway/src/client/index.ts:202-210;
//     :269-311 校验要求 strict 编解码器——透传 parse 即合法,:790-805）,
//     调用面 ctx.remote.ferrymanBlocked.<method>（:749-754 remote.<ns> 服务键）。
//     描述符参数 wire 名必须与宿主方法的 Function.toString 参数名一一对应
//     （gateway/src/index.ts:1434-1468 SRC 解析）——两半面一致性由
//     test/blocked.test.ts 跨面钉死。
//   - 卡面位置：conversation.input.dock 列表位（kind:'list', scope:'session',
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
  var DOCK_SLOT = 'conversation.input.dock';
  var POLL_MS = 4000;
  var PREVIEW_CAP = 120; // 原话预览码点上限（mock 说明:超长截断）

  /** 透传 strict 编解码器（client $mount 校验只看形状,parse 恒原值） */
  var PASS_CODEC = {
    mode: 'strict',
    typeSymbol: 'ferryman-dsh/json',
    create: function () { return { parse: function (v) { return v; } }; },
  };

  function param(name) {
    return { name: name, wire: name, source: 'json', codec: PASS_CODEC };
  }

  /** 与宿主 buildBlockedService 三方法一一同构（wire 名=宿主参数名） */
  var DESCRIPTORS = [
    {
      id: 'ferryman-dsh#list', service: NAMESPACE, namespace: NAMESPACE, method: 'list',
      invocation: { kind: 'direct' }, parameters: [param('sessionId')], result: PASS_CODEC,
    },
    {
      id: 'ferryman-dsh#resend', service: NAMESPACE, namespace: NAMESPACE, method: 'resend',
      invocation: { kind: 'direct' }, parameters: [param('id')], result: PASS_CODEC,
    },
    {
      id: 'ferryman-dsh#newSession', service: NAMESPACE, namespace: NAMESPACE, method: 'newSession',
      invocation: { kind: 'direct' }, parameters: [param('id')], result: PASS_CODEC,
    },
  ];

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
      },
      quote: {
        borderLeft: '3px solid var(--dsw-alias-border-l2, #3a4160)',
        background: 'var(--dsw-alias-bg-layer-1, rgba(255,255,255,0.03))',
        padding: '7px 10px', borderRadius: '0 8px 8px 0',
        fontSize: '13px', marginBottom: '11px', wordBreak: 'break-all',
        color: 'var(--dsw-alias-label-primary, #e8eaf2)',
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

    function makeFace(remote, mount, sessionId) {
      function call(method, arg) {
        return mount.then(function (mounted) {
          if (!mounted) return { ok: false, error: 'Remote 面未挂上' };
          var ns = remote[NAMESPACE];
          var fn = ns && ns[method];
          if (typeof fn !== 'function') {
            return { ok: false, error: '宿主未暴露 ferrymanBlocked 面（宿主侧插件需同版升级）' };
          }
          return fn(arg);
        }).catch(function (e) {
          return { ok: false, error: (e && e.message) ? e.message : String(e) };
        });
      }
      return {
        sessionId: sessionId,
        list: function () { return call('list', sessionId); },
        resend: function (id) { return call('resend', id); },
        newSession: function (id) { return call('newSession', id); },
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
      var remote = ctx && ctx.remote;
      var slots = ctx && ctx.slots;
      if (!remote || typeof remote.$mount !== 'function' || !slots
        || typeof slots.inject !== 'function' || typeof slots.register !== 'function') {
        // 非 web 壳或老宿主缺面:本半面自降级（宿主侧拦截/票10 文案不受影响）
        return;
      }
      var mount = remote.$mount({ package: 'ferryman-dsh', descriptors: DESCRIPTORS })
        .then(function () { return true; }, function (e) {
          (console.warn || function () {}).call(console, 'ferryman-dsh client: $mount 失败', e);
          return false;
        });

      function registerDock() {
        return slots.inject(DOCK_SLOT, function () {
          return slots.register({
            name: DOCK_SLOT,
            id: 'ferryman-blocked',
            order: 15, // TodoDock(0) / GoalBar(10) 之下、QueueDock(20) 之上
            inject: function (sessionId) {
              return { ferrymanBlocked: makeFace(remote, mount, sessionId) };
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

    return { name: 'ferryman-dsh', inject: ['remote', 'slots'], apply: apply };
  }
})(typeof window !== 'undefined' ? window : globalThis);
