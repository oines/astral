import http from 'node:http'
// Deterministic, loopback-only development fixture. No external model calls.
// [file] exercises real Computer execution; [slow], [retry], and [silent]
// exercise cancellation, attempt replacement, and no_reply.
const port = Number(process.env.ASTRAL_FIXTURE_PORT || 4556)
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
 else if(last.role==='tool'){
  const native=typeof last.content==='string'?last.content:JSON.stringify(last.content)
  const uri=/astral-file:\/\/[^\s"\\]+/.exec(native)?.[0]
  message={role:'assistant',content:uri?`Created the file on your Computer.\n\n[Open the validation note](${uri})\n\nThe command output and source are available in the activity details.`:`The tool completed.\n\n${native.slice(0,200)}`}
 }else if(text.includes('[file]')) message=tools('exec_command',{cmd:'printf \'# Astral validation\\n\\nThis file was written by the real Computer daemon.\\n\' > validation.md\ncurl -fsS -X POST "$ASTRAL_API_URL/v1/files/reference" -H \'Content-Type: application/json\' --data \'{"path":"validation.md"}\'',yield_time_ms:1000,max_output_tokens:1000})
 else message={role:'assistant',content:images.length?`Received **${images.length} native image block(s)** in the model request. The original attachment is on your Computer, and its model payload is preserved in this conversation.`:'The local model connection is working.\n\nThis reply came through the **real Server, model adapter and persistent Core session**.\n\n```js\nconst ready = true\n```'}
 const finish=message.tool_calls?'tool_calls':'stop'
 if(!body.stream){res.setHeader('Content-Type','application/json');return res.end(JSON.stringify({id:`reply_${count}`,choices:[{index:0,finish_reason:finish,message}],usage:{prompt_tokens:180,completion_tokens:65}}))}
 res.setHeader('Content-Type','text/event-stream');res.setHeader('Cache-Control','no-cache');res.flushHeaders()
 const send=(delta,reason=null)=>res.write(`data: ${JSON.stringify({id:`reply_${count}`,choices:[{index:0,delta,finish_reason:reason}]})}\n\n`)
 send({role:'assistant'})
 if(text.includes('[retry]')&&!retried.has(text)) {retried.add(text);send({content:'UNCOMMITTED_GHOST'});return setTimeout(()=>res.destroy(),300)}
 if(text.includes('[slow]'))await new Promise(r=>setTimeout(r,5000))
 if(message.tool_calls){
  const call=message.tool_calls[0];send({tool_calls:[{index:0,id:call.id,type:'function',function:{name:call.function.name,arguments:''}}]})
  for(let i=0;i<call.function.arguments.length;i+=50){if(res.destroyed)return;send({tool_calls:[{index:0,function:{arguments:call.function.arguments.slice(i,i+50)}}]});await new Promise(r=>setTimeout(r,90))}
 }else for(let i=0;i<message.content.length;i+=15){if(res.destroyed)return;send({content:message.content.slice(i,i+15)});await new Promise(r=>setTimeout(r,55))}
 send({},finish);res.write(`data: ${JSON.stringify({id:`reply_${count}`,choices:[],usage:{prompt_tokens:180,completion_tokens:65}})}\n\ndata: [DONE]\n\n`);res.end()
}).listen(port,'127.0.0.1',()=>console.log(`Local fixture provider listening on 127.0.0.1:${port}`))
