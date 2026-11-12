import {test} from "node:test";
import assert from "node:assert/strict";
import {renderPRPreview} from "../dist/control-ui/v1/pr-preview.js";
const node=(tag,props={},...kids)=>({tag,...props,children:props.textContent!==undefined?[props.textContent]:kids,append(...more){this.children.push(...more);}});
const all=n=>typeof n==="string"?[]:[n,...n.children.flatMap(all)];
test("Markdown headings, lists, emphasis, code, quotes and tables render as DOM nodes",()=>{
 const body="# Summary\n\n**Bold** and `code`.\n\n- [x] Done\n- Pending\n\n> A quote\n\n```js\nconst ok = true;\n```\n\n| Name | State |\n|---|---|\n| api | ready |";
 const result=all(renderPRPreview(body,node));
 assert.equal(result.filter(n=>n.className==="wtc-preview-task").length,1);
 assert.ok(!result.some(n=>n.children.some(c=>typeof c==="string"&&c.includes("[x]"))));
 for(const tag of ["h2","strong","code","ul","li","blockquote","pre","table","th","td"])assert.ok(result.some(n=>n.tag===tag),tag);
});
test("previewing arbitrary HTML, links and images cannot execute or fetch resources",()=>{
 const body='<script>alert(1)</script>\n\n<img src="https://private.invalid/pixel" onerror="alert(1)">\n\n[bad](javascript:alert(1)) [encoded](java&#x73;cript:alert(1)) ![photo](https://example.invalid/image.png) [safe](https://example.invalid/path)';
 const result=all(renderPRPreview(body,node));
 assert.ok(!result.some(n=>["script","img","iframe","object","style"].includes(n.tag)));
 assert.deepEqual(result.filter(n=>n.tag==="a").map(n=>n.href),["https://example.invalid/image.png","https://example.invalid/path"]);
 for(const link of result.filter(n=>n.tag==="a"))assert.equal(link.rel,"noopener noreferrer");
 assert.ok(!result.some(n=>n.src||n.innerHTML||n.onclick));
});
