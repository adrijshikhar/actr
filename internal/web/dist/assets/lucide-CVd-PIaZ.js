function nt(a,y){for(var l=0;l<y.length;l++){const d=y[l];if(typeof d!="string"&&!Array.isArray(d)){for(const _ in d)if(_!=="default"&&!(_ in a)){const h=Object.getOwnPropertyDescriptor(d,_);h&&Object.defineProperty(a,_,h.get?h:{enumerable:!0,get:()=>d[_]})}}}return Object.freeze(Object.defineProperty(a,Symbol.toStringTag,{value:"Module"}))}function ot(a){return a&&a.__esModule&&Object.prototype.hasOwnProperty.call(a,"default")?a.default:a}var O={exports:{}},o={};/**
 * @license React
 * react.production.js
 *
 * Copyright (c) Meta Platforms, Inc. and affiliates.
 *
 * This source code is licensed under the MIT license found in the
 * LICENSE file in the root directory of this source tree.
 */var K;function ut(){if(K)return o;K=1;var a=Symbol.for("react.transitional.element"),y=Symbol.for("react.portal"),l=Symbol.for("react.fragment"),d=Symbol.for("react.strict_mode"),_=Symbol.for("react.profiler"),h=Symbol.for("react.consumer"),R=Symbol.for("react.context"),T=Symbol.for("react.forward_ref"),w=Symbol.for("react.suspense"),x=Symbol.for("react.memo"),C=Symbol.for("react.lazy"),W=Symbol.for("react.activity"),G=Symbol.for("react.view_transition"),H=Symbol.iterator;function X(t){return t===null||typeof t!="object"?null:(t=H&&t[H]||t["@@iterator"],typeof t=="function"?t:null)}var P={isMounted:function(){return!1},enqueueForceUpdate:function(){},enqueueReplaceState:function(){},enqueueSetState:function(){}},b=Object.assign,L={};function E(t,e,r){this.props=t,this.context=e,this.refs=L,this.updater=r||P}E.prototype.isReactComponent={},E.prototype.setState=function(t,e){if(typeof t!="object"&&typeof t!="function"&&t!=null)throw Error("takes an object of state variables to update or a function which returns an object of state variables.");this.updater.enqueueSetState(this,t,e,"setState")},E.prototype.forceUpdate=function(t){this.updater.enqueueForceUpdate(this,t,"forceUpdate")};function q(){}q.prototype=E.prototype;function A(t,e,r){this.props=t,this.context=e,this.refs=L,this.updater=r||P}var M=A.prototype=new q;M.constructor=A,b(M,E.prototype),M.isPureReactComponent=!0;var I=Array.isArray;function S(){}var i={H:null,A:null,T:null,S:null},z=Object.prototype.hasOwnProperty;function $(t,e,r){var n=r.ref;return{$$typeof:a,type:t,key:e,ref:n!==void 0?n:null,props:r}}function Q(t,e){return $(t.type,e,t.props)}function N(t){return typeof t=="object"&&t!==null&&t.$$typeof===a}function J(t){var e={"=":"=0",":":"=2"};return"$"+t.replace(/[=:]/g,function(r){return e[r]})}var Y=/\/+/g;function j(t,e){return typeof t=="object"&&t!==null&&t.key!=null?J(""+t.key):e.toString(36)}function F(t){switch(t.status){case"fulfilled":return t.value;case"rejected":throw t.reason;default:switch(typeof t.status=="string"?t.then(S,S):(t.status="pending",t.then(function(e){t.status==="pending"&&(t.status="fulfilled",t.value=e)},function(e){t.status==="pending"&&(t.status="rejected",t.reason=e)})),t.status){case"fulfilled":return t.value;case"rejected":throw t.reason}}throw t}function m(t,e,r,n,u){var s=typeof t;(s==="undefined"||s==="boolean")&&(t=null);var c=!1;if(t===null)c=!0;else switch(s){case"bigint":case"string":case"number":c=!0;break;case"object":switch(t.$$typeof){case a:case y:c=!0;break;case C:return c=t._init,m(c(t._payload),e,r,n,u)}}if(c)return u=u(t),c=n===""?"."+j(t,0):n,I(u)?(r="",c!=null&&(r=c.replace(Y,"$&/")+"/"),m(u,e,r,"",function(rt){return rt})):u!=null&&(N(u)&&(u=Q(u,r+(u.key==null||t&&t.key===u.key?"":(""+u.key).replace(Y,"$&/")+"/")+c)),e.push(u)),1;c=0;var v=n===""?".":n+":";if(I(t))for(var p=0;p<t.length;p++)n=t[p],s=v+j(n,p),c+=m(n,e,r,s,u);else if(p=X(t),typeof p=="function")for(t=p.call(t),p=0;!(n=t.next()).done;)n=n.value,s=v+j(n,p++),c+=m(n,e,r,s,u);else if(s==="object"){if(typeof t.then=="function")return m(F(t),e,r,n,u);throw e=String(t),Error("Objects are not valid as a React child (found: "+(e==="[object Object]"?"object with keys {"+Object.keys(t).join(", ")+"}":e)+"). If you meant to render a collection of children, use an array instead.")}return c}function g(t,e,r){if(t==null)return t;var n=[],u=0;return m(t,n,"","",function(s){return e.call(r,s,u++)}),n}function tt(t){if(t._status===-1){var e=t._result,r=e();r.then(function(n){(t._status===0||t._status===-1)&&(t._status=1,t._result=n,r.status===void 0&&(r.status="fulfilled",r.value=n))},function(n){(t._status===0||t._status===-1)&&(t._status=2,t._result=n,r.status===void 0&&(r.status="rejected",r.reason=n))}),t._status===-1&&(t._status=0,t._result=r)}if(t._status===1)return t._result.default;throw t._result}var D=typeof reportError=="function"?reportError:function(t){if(typeof window=="object"&&typeof window.ErrorEvent=="function"){var e=new window.ErrorEvent("error",{bubbles:!0,cancelable:!0,message:typeof t=="object"&&t!==null&&typeof t.message=="string"?String(t.message):String(t),error:t});if(!window.dispatchEvent(e))return}else if(typeof process=="object"&&typeof process.emit=="function"){process.emit("uncaughtException",t);return}console.error(t)};function U(t){var e=i.T,r={};r.types=e!==null?e.types:null,i.T=r;try{var n=t(),u=i.S;u!==null&&u(r,n),typeof n=="object"&&n!==null&&typeof n.then=="function"&&n.then(S,D)}catch(s){D(s)}finally{e!==null&&r.types!==null&&(e.types=r.types),i.T=e}}function B(t){var e=i.T;if(e!==null){var r=e.types;r===null?e.types=[t]:r.indexOf(t)===-1&&r.push(t)}else U(B.bind(null,t))}var et={map:g,forEach:function(t,e,r){g(t,function(){e.apply(this,arguments)},r)},count:function(t){var e=0;return g(t,function(){e++}),e},toArray:function(t){return g(t,function(e){return e})||[]},only:function(t){if(!N(t))throw Error("React.Children.only expected to receive a single React element child.");return t}};return o.Activity=W,o.Children=et,o.Component=E,o.Fragment=l,o.Profiler=_,o.PureComponent=A,o.StrictMode=d,o.Suspense=w,o.ViewTransition=G,o.__CLIENT_INTERNALS_DO_NOT_USE_OR_WARN_USERS_THEY_CANNOT_UPGRADE=i,o.__COMPILER_RUNTIME={__proto__:null,c:function(t){return i.H.useMemoCache(t)}},o.addTransitionType=B,o.cache=function(t){return function(){return t.apply(null,arguments)}},o.cacheSignal=function(){return null},o.cloneElement=function(t,e,r){if(t==null)throw Error("The argument must be a React element, but you passed "+t+".");var n=b({},t.props),u=t.key;if(e!=null)for(s in e.key!==void 0&&(u=""+e.key),e)!z.call(e,s)||s==="key"||s==="__self"||s==="__source"||s==="ref"&&e.ref===void 0||(n[s]=e[s]);var s=arguments.length-2;if(s===1)n.children=r;else if(1<s){for(var c=Array(s),v=0;v<s;v++)c[v]=arguments[v+2];n.children=c}return $(t.type,u,n)},o.createContext=function(t){return t={$$typeof:R,_currentValue:t,_currentValue2:t,_threadCount:0,Provider:null,Consumer:null},t.Provider=t,t.Consumer={$$typeof:h,_context:t},t},o.createElement=function(t,e,r){var n,u={},s=null;if(e!=null)for(n in e.key!==void 0&&(s=""+e.key),e)z.call(e,n)&&n!=="key"&&n!=="__self"&&n!=="__source"&&(u[n]=e[n]);var c=arguments.length-2;if(c===1)u.children=r;else if(1<c){for(var v=Array(c),p=0;p<c;p++)v[p]=arguments[p+2];u.children=v}if(t&&t.defaultProps)for(n in c=t.defaultProps,c)u[n]===void 0&&(u[n]=c[n]);return $(t,s,u)},o.createRef=function(){return{current:null}},o.forwardRef=function(t){return{$$typeof:T,render:t}},o.isValidElement=N,o.lazy=function(t){return{$$typeof:C,_payload:{_status:-1,_result:t},_init:tt}},o.memo=function(t,e){return{$$typeof:x,type:t,compare:e===void 0?null:e}},o.startTransition=U,o.unstable_useCacheRefresh=function(){return i.H.useCacheRefresh()},o.use=function(t){return i.H.use(t)},o.useActionState=function(t,e,r){return i.H.useActionState(t,e,r)},o.useCallback=function(t,e){return i.H.useCallback(t,e)},o.useContext=function(t){return i.H.useContext(t)},o.useDebugValue=function(){},o.useDeferredValue=function(t,e){return i.H.useDeferredValue(t,e)},o.useEffect=function(t,e){return i.H.useEffect(t,e)},o.useEffectEvent=function(t){return i.H.useEffectEvent(t)},o.useId=function(){return i.H.useId()},o.useImperativeHandle=function(t,e,r){return i.H.useImperativeHandle(t,e,r)},o.useInsertionEffect=function(t,e){return i.H.useInsertionEffect(t,e)},o.useLayoutEffect=function(t,e){return i.H.useLayoutEffect(t,e)},o.useMemo=function(t,e){return i.H.useMemo(t,e)},o.useOptimistic=function(t,e){return i.H.useOptimistic(t,e)},o.useReducer=function(t,e,r){return i.H.useReducer(t,e,r)},o.useRef=function(t){return i.H.useRef(t)},o.useState=function(t){return i.H.useState(t)},o.useSyncExternalStore=function(t,e,r){return i.H.useSyncExternalStore(t,e,r)},o.useTransition=function(){return i.H.useTransition()},o.version="19.3.1",o}var Z;function st(){return Z||(Z=1,O.exports=ut()),O.exports}var k=st();const it=ot(k),Mt=nt({__proto__:null,default:it},[k]);/**
 * @license lucide-react v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const ct=a=>a.replace(/([a-z0-9])([A-Z])/g,"$1-$2").toLowerCase(),V=(...a)=>a.filter((y,l,d)=>!!y&&y.trim()!==""&&d.indexOf(y)===l).join(" ").trim();/**
 * @license lucide-react v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */var at={xmlns:"http://www.w3.org/2000/svg",width:24,height:24,viewBox:"0 0 24 24",fill:"none",stroke:"currentColor",strokeWidth:2,strokeLinecap:"round",strokeLinejoin:"round"};/**
 * @license lucide-react v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const ft=k.forwardRef(({color:a="currentColor",size:y=24,strokeWidth:l=2,absoluteStrokeWidth:d,className:_="",children:h,iconNode:R,...T},w)=>k.createElement("svg",{ref:w,...at,width:y,height:y,stroke:a,strokeWidth:d?Number(l)*24/Number(y):l,className:V("lucide",_),...T},[...R.map(([x,C])=>k.createElement(x,C)),...Array.isArray(h)?h:[h]]));/**
 * @license lucide-react v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const f=(a,y)=>{const l=k.forwardRef(({className:d,..._},h)=>k.createElement(ft,{ref:h,iconNode:y,className:V(`lucide-${ct(a)}`,d),..._}));return l.displayName=`${a}`,l};/**
 * @license lucide-react v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const yt=[["path",{d:"M22 12h-2.48a2 2 0 0 0-1.93 1.46l-2.35 8.36a.25.25 0 0 1-.48 0L9.24 2.18a.25.25 0 0 0-.48 0l-2.35 8.36A2 2 0 0 1 4.49 12H2",key:"169zse"}]],St=f("Activity",yt);/**
 * @license lucide-react v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const pt=[["path",{d:"M2.97 12.92A2 2 0 0 0 2 14.63v3.24a2 2 0 0 0 .97 1.71l3 1.8a2 2 0 0 0 2.06 0L12 19v-5.5l-5-3-4.03 2.42Z",key:"lc1i9w"}],["path",{d:"m7 16.5-4.74-2.85",key:"1o9zyk"}],["path",{d:"m7 16.5 5-3",key:"va8pkn"}],["path",{d:"M7 16.5v5.17",key:"jnp8gn"}],["path",{d:"M12 13.5V19l3.97 2.38a2 2 0 0 0 2.06 0l3-1.8a2 2 0 0 0 .97-1.71v-3.24a2 2 0 0 0-.97-1.71L17 10.5l-5 3Z",key:"8zsnat"}],["path",{d:"m17 16.5-5-3",key:"8arw3v"}],["path",{d:"m17 16.5 4.74-2.85",key:"8rfmw"}],["path",{d:"M17 16.5v5.17",key:"k6z78m"}],["path",{d:"M7.97 4.42A2 2 0 0 0 7 6.13v4.37l5 3 5-3V6.13a2 2 0 0 0-.97-1.71l-3-1.8a2 2 0 0 0-2.06 0l-3 1.8Z",key:"1xygjf"}],["path",{d:"M12 8 7.26 5.15",key:"1vbdud"}],["path",{d:"m12 8 4.74-2.85",key:"3rx089"}],["path",{d:"M12 13.5V8",key:"1io7kd"}]],$t=f("Boxes",pt);/**
 * @license lucide-react v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const lt=[["path",{d:"m9 18 6-6-6-6",key:"mthhwq"}]],Nt=f("ChevronRight",lt);/**
 * @license lucide-react v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const dt=[["circle",{cx:"12",cy:"12",r:"10",key:"1mglay"}],["line",{x1:"12",x2:"12",y1:"8",y2:"12",key:"1pkeuh"}],["line",{x1:"12",x2:"12.01",y1:"16",y2:"16",key:"4dfq90"}]],jt=f("CircleAlert",dt);/**
 * @license lucide-react v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const _t=[["circle",{cx:"12",cy:"12",r:"10",key:"1mglay"}],["path",{d:"m9 12 2 2 4-4",key:"dzmm74"}]],Ot=f("CircleCheck",_t);/**
 * @license lucide-react v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const ht=[["circle",{cx:"12",cy:"12",r:"10",key:"1mglay"}],["path",{d:"m15 9-6 6",key:"1uzhvr"}],["path",{d:"m9 9 6 6",key:"z0biqf"}]],Ht=f("CircleX",ht);/**
 * @license lucide-react v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const vt=[["circle",{cx:"12",cy:"12",r:"10",key:"1mglay"}],["polyline",{points:"12 6 12 12 16 14",key:"68esgv"}]],Pt=f("Clock",vt);/**
 * @license lucide-react v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const kt=[["rect",{width:"16",height:"16",x:"4",y:"4",rx:"2",key:"14l7u7"}],["rect",{width:"6",height:"6",x:"9",y:"9",rx:"1",key:"5aljv4"}],["path",{d:"M15 2v2",key:"13l42r"}],["path",{d:"M15 20v2",key:"15mkzm"}],["path",{d:"M2 15h2",key:"1gxd5l"}],["path",{d:"M2 9h2",key:"1bbxkp"}],["path",{d:"M20 15h2",key:"19e6y8"}],["path",{d:"M20 9h2",key:"19tzq7"}],["path",{d:"M9 2v2",key:"165o2o"}],["path",{d:"M9 20v2",key:"i2bqo8"}]],bt=f("Cpu",kt);/**
 * @license lucide-react v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const Et=[["line",{x1:"22",x2:"2",y1:"12",y2:"12",key:"1y58io"}],["path",{d:"M5.45 5.11 2 12v6a2 2 0 0 0 2 2h16a2 2 0 0 0 2-2v-6l-3.45-6.89A2 2 0 0 0 16.76 4H7.24a2 2 0 0 0-1.79 1.11z",key:"oot6mr"}],["line",{x1:"6",x2:"6.01",y1:"16",y2:"16",key:"sgf278"}],["line",{x1:"10",x2:"10.01",y1:"16",y2:"16",key:"1l4acy"}]],Lt=f("HardDrive",Et);/**
 * @license lucide-react v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const mt=[["path",{d:"M3 12a9 9 0 1 0 9-9 9.75 9.75 0 0 0-6.74 2.74L3 8",key:"1357e3"}],["path",{d:"M3 3v5h5",key:"1xhq8a"}],["path",{d:"M12 7v5l4 2",key:"1fdv2h"}]],qt=f("History",mt);/**
 * @license lucide-react v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const Ct=[["path",{d:"M2.586 17.414A2 2 0 0 0 2 18.828V21a1 1 0 0 0 1 1h3a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h1a1 1 0 0 0 1-1v-1a1 1 0 0 1 1-1h.172a2 2 0 0 0 1.414-.586l.814-.814a6.5 6.5 0 1 0-4-4z",key:"1s6t7t"}],["circle",{cx:"16.5",cy:"7.5",r:".5",fill:"currentColor",key:"w0ekpg"}]],It=f("KeyRound",Ct);/**
 * @license lucide-react v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const gt=[["rect",{width:"7",height:"9",x:"3",y:"3",rx:"1",key:"10lvy0"}],["rect",{width:"7",height:"5",x:"14",y:"3",rx:"1",key:"16une8"}],["rect",{width:"7",height:"9",x:"14",y:"12",rx:"1",key:"1hutg5"}],["rect",{width:"7",height:"5",x:"3",y:"16",rx:"1",key:"ldoo1y"}]],zt=f("LayoutDashboard",gt);/**
 * @license lucide-react v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const Rt=[["path",{d:"M12 2v10",key:"mnfbl"}],["path",{d:"M18.4 6.6a9 9 0 1 1-12.77.04",key:"obofu9"}]],Yt=f("Power",Rt);/**
 * @license lucide-react v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const Tt=[["path",{d:"M3 12a9 9 0 0 1 9-9 9.75 9.75 0 0 1 6.74 2.74L21 8",key:"v9h5vc"}],["path",{d:"M21 3v5h-5",key:"1q7to0"}],["path",{d:"M21 12a9 9 0 0 1-9 9 9.75 9.75 0 0 1-6.74-2.74L3 16",key:"3uifl3"}],["path",{d:"M8 16H3v5",key:"1cv678"}]],Dt=f("RefreshCw",Tt);/**
 * @license lucide-react v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const wt=[["rect",{width:"20",height:"8",x:"2",y:"2",rx:"2",ry:"2",key:"ngkwjq"}],["rect",{width:"20",height:"8",x:"2",y:"14",rx:"2",ry:"2",key:"iecqi9"}],["line",{x1:"6",x2:"6.01",y1:"6",y2:"6",key:"16zg32"}],["line",{x1:"6",x2:"6.01",y1:"18",y2:"18",key:"nzw8ys"}]],Ut=f("Server",wt);/**
 * @license lucide-react v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const xt=[["path",{d:"M20 13c0 5-3.5 7.5-7.66 8.95a1 1 0 0 1-.67-.01C7.5 20.5 4 18 4 13V6a1 1 0 0 1 1-1c2 0 4.5-1.2 6.24-2.72a1.17 1.17 0 0 1 1.52 0C14.51 3.81 17 5 19 5a1 1 0 0 1 1 1z",key:"oel41y"}]],Bt=f("Shield",xt);/**
 * @license lucide-react v0.475.0 - ISC
 *
 * This source code is licensed under the ISC license.
 * See the LICENSE file in the root directory of this source tree.
 */const At=[["polyline",{points:"4 17 10 11 4 5",key:"akl6gq"}],["line",{x1:"12",x2:"20",y1:"19",y2:"19",key:"q2wloq"}]],Kt=f("Terminal",At);export{St as A,$t as B,Nt as C,qt as H,It as K,zt as L,Yt as P,Mt as R,Ut as S,Kt as T,st as a,Dt as b,jt as c,Pt as d,bt as e,Lt as f,ot as g,Ot as h,Ht as i,Bt as j,it as k,k as r};
