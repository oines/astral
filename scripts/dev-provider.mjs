import http from 'node:http'
// Deterministic, loopback-only development fixture. No external model calls.
// [file] exercises real Computer execution; [slow], [retry], and [silent]
// exercise cancellation, attempt replacement, and no_reply.
const port = Number(process.env.ASTRAL_FIXTURE_PORT || 4556)
const layoutReply = `已整理好本周工作笔记，完成事项和下一步安排如下。

## 本周进展

- **界面整理**：统一消息正文、工具记录与输入框的间距。
- **交互检查**：确认长消息自动增高，发送后恢复单行。
- **数据保留**：模型选择、附件和历史会话继续可用。

## 接下来

1. 检查浅色与深色主题下的阅读体验。
2. 验证窄窗口中的换行和滚动。
3. 将确认后的改动整理到工作记录。

> 每个步骤都保留明确的结果，方便继续跟进。

| 项目 | 状态 |
| --- | --- |
| 输入框 | 已完成 |
| 消息排版 | 已检查 |

可以在工作目录中通过 \`notes.md\` 查看记录。

\`\`\`sh
cat notes.md
\`\`\``
let count=0
const retried=new Set()
const diagnostics=[]
const tools=(name,args)=>({role:'assistant',content:null,tool_calls:[{id:`call_${count}`,type:'function',function:{name,arguments:JSON.stringify(args)}}]})
http.createServer(async(req,res)=>{
 if(req.method==='GET'){
  res.setHeader('Content-Type','application/json')
  if(req.url==='/diagnostics')return res.end(JSON.stringify(diagnostics))
  return res.end(JSON.stringify({data:[{id:'astral-fixture',name:'Local validation model',context_window:128000,max_output_tokens:4096,input_modalities:['text','image'],effort:{supported_levels:['low','high'],default_level:'low'}}]}))
 }
 const chunks=[];for await(const c of req)chunks.push(c)
 let body;try{body=JSON.parse(Buffer.concat(chunks).toString())}catch{return res.writeHead(400).end()}
 count++
 const messages=body.messages||[]
 const last=messages.at(-1)||{}
 const users=messages.filter(m=>m.role==='user')
 const user=users.at(-1)||{}
 const text=typeof user.content==='string'?user.content:JSON.stringify(user.content||'')
 const images=messages.flatMap(m=>Array.isArray(m.content)?m.content:[]).filter(p=>p.type==='image_url')
 diagnostics.push({request:count,stream:body.stream,image_count:images.length,native_data:images.every(p=>p.image_url?.url?.startsWith('data:image/')),reasoning_effort:body.reasoning_effort||null,last_role:last.role})
 let message
 if(JSON.stringify(body.tools||[]).includes('astral_verify')) message=body.tool_choice==='none'?{role:'assistant',content:'pong'}:tools('astral_verify',{value:'ping'})
 else if(!body.tools?.length) message={role:'assistant',content:'The conversation validated Computer execution, stable file references and preserved native image inputs. Continue from the latest user request.'}
 else if(text.includes('[silent]')) message=tools('no_reply',{})
 else if(text.includes('[process]')) {
  const completed=messages.slice(messages.lastIndexOf(user)+1).filter(m=>m.role==='tool').length
  if(completed===0)message={...tools('exec_command',{cmd:"printf 'notes.md\\n'",yield_time_ms:1000,max_output_tokens:1000}),reasoning_content:'先查看工作记录，再逐项核对本周已完成的事项。'}
  else if(completed===1)message={...tools('exec_command',{cmd:"printf '完成事项已核对。\\n'",yield_time_ms:1000,max_output_tokens:1000}),reasoning_content:'已找到工作记录，继续确认每项结果与下一步安排。'}
  else if(completed===2)message={...tools('exec_command',{cmd:"printf '格式检查通过。\\n'",yield_time_ms:1000,max_output_tokens:1000}),content:'已核对完成事项，接下来整理笔记的段落和列表。'}
  else message={role:'assistant',content:layoutReply}
 }
 else if(last.role==='tool' && text.includes('本周工作笔记')) message={role:'assistant',content:layoutReply}
 else if(text.includes('本周工作笔记')) message=tools('exec_command',{cmd:'printf \'notes.md\\n工作笔记已整理。\\n\'',yield_time_ms:1000,max_output_tokens:1000})
 else if(last.role==='tool'){
  const native=typeof last.content==='string'?last.content:JSON.stringify(last.content)
  const uri=/astral-file:\/\/[^\s"\\]+/.exec(native)?.[0]
  message={role:'assistant',content:uri?`Created the file on your Computer.\n\n[Open the validation note](${uri})\n\nThe command output and source are available in the activity details.`:`The tool completed.\n\n${native.slice(0,200)}`}
 }else if(text.includes('[file]')) message=tools('exec_command',{cmd:'fixture_dir=$(mktemp -d)\nprintf \'# Astral validation\\n\\nThis file was written by the real Computer daemon.\\n\' > "$fixture_dir/validation.md"\ncurl -fsS -X POST "$ASTRAL_API_URL/v1/files/reference" -H \'Content-Type: application/json\' --data "{\\"path\\":\\"$fixture_dir/validation.md\\"}"',yield_time_ms:1000,max_output_tokens:1000})
 else message={role:'assistant',content:images.length?`Received **${images.length} native image block(s)** in the model request. The original attachment is on your Computer, and its model payload is preserved in this conversation.`:'The local model connection is working.\n\nThis reply came through the **real Server, model adapter and persistent Core session**.\n\n```js\nconst ready = true\n```'}
 const finish=message.tool_calls?'tool_calls':'stop'
 if(!body.stream){res.setHeader('Content-Type','application/json');return res.end(JSON.stringify({id:`reply_${count}`,choices:[{index:0,finish_reason:finish,message}],usage:{prompt_tokens:180,completion_tokens:65}}))}
 res.setHeader('Content-Type','text/event-stream');res.setHeader('Cache-Control','no-cache');res.flushHeaders()
 const send=(delta,reason=null)=>res.write(`data: ${JSON.stringify({id:`reply_${count}`,choices:[{index:0,delta,finish_reason:reason}]})}\n\n`)
 send({role:'assistant'})
 if(text.includes('[retry]')&&!retried.has(text)) {retried.add(text);send({content:'UNCOMMITTED_GHOST'});return setTimeout(()=>res.destroy(),300)}
 if(text.includes('[slow]'))await new Promise(r=>setTimeout(r,5000))
 if(message.reasoning_content)send({reasoning_content:message.reasoning_content})
 if(message.tool_calls){
  if(message.content)send({content:message.content})
  const call=message.tool_calls[0];send({tool_calls:[{index:0,id:call.id,type:'function',function:{name:call.function.name,arguments:''}}]})
  for(let i=0;i<call.function.arguments.length;i+=50){if(res.destroyed)return;send({tool_calls:[{index:0,function:{arguments:call.function.arguments.slice(i,i+50)}}]});await new Promise(r=>setTimeout(r,90))}
 }else for(let i=0;i<message.content.length;i+=15){if(res.destroyed)return;send({content:message.content.slice(i,i+15)});await new Promise(r=>setTimeout(r,55))}
 send({},finish);res.write(`data: ${JSON.stringify({id:`reply_${count}`,choices:[],usage:{prompt_tokens:180,completion_tokens:65}})}\n\ndata: [DONE]\n\n`);res.end()
}).listen(port,'127.0.0.1',()=>console.log(`Local fixture provider listening on 127.0.0.1:${port}`))
