#!/usr/bin/env python3
"""Exercise the compiled program over documented IPC frames; no network/framework access."""
import json,pathlib,queue,subprocess,sys,tempfile,threading,time
binary=pathlib.Path(sys.argv[1]).resolve()
manifest=json.loads(pathlib.Path(sys.argv[2]).read_text())
robot,user=10000001,20000002
with tempfile.TemporaryDirectory(prefix='laoluo-ipc-') as tmp:
 (pathlib.Path(tmp)/'managed-data').mkdir()
 config=pathlib.Path(tmp)/'managed-data/config.json';config.write_text(json.dumps({'bot_qq':robot,'data_dir':'data','quick_login':True,'web_listen':'127.0.0.1:0'}))
 connection=pathlib.Path(tmp)/'connection.json';connection.write_text(json.dumps({'schema':1,'transport':'native-ipc-v1','framework_api':'3.0.0','instance_id':'fixture-instance','data_directory':str(pathlib.Path(tmp)/'managed-data')}))
 import os
 env={**os.environ,'MENGKA_PLUGIN_CONNECTION_FILE':str(connection)}
 p=subprocess.Popen([str(binary),'--config',str(config)],stdin=subprocess.PIPE,stdout=subprocess.PIPE,stderr=subprocess.PIPE,text=True,bufsize=1,env=env)
 frames=queue.Queue();threading.Thread(target=lambda:[frames.put(json.loads(line)) for line in p.stdout],daemon=True).start()
 def send(v):p.stdin.write(json.dumps(v)+'\n');p.stdin.flush()
 def next_frame(timeout=5):return frames.get(timeout=timeout)
 def answer(q):
  action=q['action']
  assert action in manifest['runtime']['actions']
  if action=='get_plugin_context':data={'management_api_version':2,'scope':'framework','account_selector':'self_id+protocol_id','available_actions':manifest['runtime']['actions']}
  elif action=='get_account_management_context':data={'api_version':2,'scope':'framework','accounts':[{'self_id':robot,'protocol':{'id':1,'platform':'Android','type':'Pad'},'device':{'id':1},'status':1,'nt':1,'level':100}],'protocols':[{'id':1,'platform':'Android','type':'Pad','state':True}]}
  elif action=='send_friend_msg':
   assert q['params']['self_id']==robot and q['params']['protocol_id']==1 and q['params']['user_id']==user
   data={'success':True,'message_id':123}
  else:raise AssertionError('unexpected action '+action)
  send({'type':'action_result','id':q['id'],'ok':True,'data':data})
 hello=next_frame();assert hello['type']=='hello' and hello['plugin_id']==manifest['plugin_id'] and hello['version']==manifest['version'];send({'type':'ready','transport':'native-ipc-v1','api_version':'1','framework_api':'3.0.0','plugin_id':'laoluo-levelup','instance_id':'fixture-instance'})
 answer(next_frame())
 event={'type':'event','data':{'post_type':'friend_message','self_id':robot,'protocol_id':1,'sender':{'user_id':user},'message_id':1,'message':[{'type':'text','data':{'text':'等级菜单'}}]}}
 send(event)
 found=False
 for i in range(8):
  q=next_frame();answer(q)
  if q['action']=='send_friend_msg':
   text=q['params']['message'][0]['data']['text'];assert all(s in text for s in ['开始登录','立即补挂','项目开关','电脑登录']);found=True;break
 assert found
 send(event) # Same message may be redelivered; it must not send another reply.
 end=time.monotonic()+.4;replies=0
 while time.monotonic()<end:
  try:q=next_frame(.05)
  except queue.Empty:continue
  if q['action']=='send_friend_msg':replies+=1
  answer(q)
 assert replies==0
 p.stdin.close();p.wait(timeout=5)
 logs=p.stderr.read();assert '等级菜单' not in logs and 'password' not in logs.lower()
 assert p.returncode==0
 print('PASS compiled IPC hello/ready, permissions, private menu, account scope, duplicate suppression and clean shutdown; no network')
