import { marked } from "./vendor/marked.esm.js";

const safeLink = value => {
  try {const url=new URL(value);return ["https:","http:","mailto:"].includes(url.protocol)&&!url.username&&!url.password?url.href:null;}catch{return null;}
};

// Render a deliberately inert Markdown preview. The publisher receives the
// original string, including HTML; previewing it must not execute or fetch it.
export function renderPRPreview(source, el) {
  const inline=tokens=>tokens.flatMap(token=>{
    const kids=()=>inline(token.tokens??[]);
    switch(token.type){
      case "strong":return [el("strong",{},...kids())];
      case "em":return [el("em",{},...kids())];
      case "del":return [el("del",{},...kids())];
      case "codespan":return [el("code",{textContent:token.text})];
      case "br":return [el("br")];
      case "link":case "image":{
        const href=safeLink(token.href),label=token.type==="image"?[`Image: ${token.text||"open image"}`]:kids();
        return [href?el("a",{href,target:"_blank",rel:"noopener noreferrer",...(token.title?{title:token.title}:{})},...label):el("span",{},...label)];
      }
      case "escape":case "html":return [token.text??token.raw??""];
      default:return token.tokens?kids():[token.text??token.raw??""];
    }
  });
  const blocks=tokens=>tokens.flatMap(token=>{
    switch(token.type){
      case "space":return [];
      case "checkbox":return [el("span",{className:"wtc-preview-task",role:"img",ariaLabel:token.checked?"Completed task":"Incomplete task",textContent:token.checked?"☑":"☐"})];
      case "heading":return [el(`h${Math.min(6,Math.max(2,token.depth+1))}`,{},...inline(token.tokens))];
      case "paragraph":return [el("p",{},...inline(token.tokens))];
      case "text":return token.tokens?inline(token.tokens):[token.text];
      case "code":return [el("pre",{},el("code",{textContent:token.text}))];
      case "blockquote":return [el("blockquote",{},...blocks(token.tokens))];
      case "hr":return [el("hr")];
      case "list":return [el(token.ordered?"ol":"ul",token.ordered?{start:token.start}:{},...token.items.map(item=>el("li",item.task?{className:"wtc-preview-task-item"}:{},...blocks(item.tokens))))];
      case "table":return [el("div",{className:"wtc-pr-preview-table"},el("table",{},el("thead",{},el("tr",{},...token.header.map(cell=>el("th",{},...inline(cell.tokens))))),el("tbody",{},...token.rows.map(row=>el("tr",{},...row.map(cell=>el("td",{},...inline(cell.tokens))))))))];
      default:return [el("p",{textContent:token.raw??token.text??""})];
    }
  });
  const root=el("div",{className:"wtc-pr-preview-markdown"});
  try{root.append(...blocks(marked.lexer(String(source??""))));}catch{root.textContent=String(source??"");}
  return root;
}
