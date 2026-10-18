from pathlib import Path
from fastapi import FastAPI
from fastapi.testclient import TestClient

from app.routers.recorder import router
from app.services import meeting_capture as capture


def test_browser_audio_upload_status_and_playback(monkeypatch, tmp_path):
    monkeypatch.chdir(tmp_path)
    monkeypatch.setattr(capture,"process_recording",lambda path:capture.update_state(path,status="done",minutes="测试纪要"))
    app=FastAPI();app.include_router(router)
    client=TestClient(app)
    response=client.post('/api/recorder/capture',files={'file':('test.webm',b'test-audio','audio/webm')})
    assert response.status_code==200
    data=response.json()
    assert client.get(data['audio_url']).content==b'test-audio'
    assert client.get('/api/recorder/capture/'+data['filename']).json()['minutes']=='测试纪要'
    assert client.post('/api/recorder/capture',files={'file':('file.exe',b'x')}).status_code==415


def test_missing_local_model_keeps_audio(monkeypatch,tmp_path):
    monkeypatch.chdir(tmp_path)
    audio=tmp_path/'capture.webm';audio.write_bytes(b'test')
    monkeypatch.setattr(capture,'capabilities',lambda:{'local_transcription_ready':False})
    capture.process_recording(audio)
    assert audio.exists()
    assert capture.read_state(audio)['status']=='needs_setup'


def test_local_transcription_and_shared_summary(monkeypatch,tmp_path):
    from app.services import llm_client
    monkeypatch.chdir(tmp_path)
    audio=tmp_path/'capture.m4a';audio.write_bytes(b'test')
    monkeypatch.setattr(capture,'capabilities',lambda:{'local_transcription_ready':True,'model':'base'})
    monkeypatch.setattr(capture,'executable',lambda name:'/usr/bin/'+name)
    def transcribe(cmd,**kwargs):
        assert '--output_format' in cmd
        audio.with_suffix('.txt').write_text('小王负责跟进，周五前完成。')
    monkeypatch.setattr(capture.subprocess,'run',transcribe)
    monkeypatch.setattr(llm_client,'siliconflow_chat',lambda *args,**kwargs:'## 待办\n小王：周五前完成跟进。')
    capture.process_recording(audio)
    state=capture.read_state(audio)
    assert state['status']=='done'
    assert '小王' in state['minutes']


def test_retry_reuses_transcript_after_restart(monkeypatch, tmp_path):
    from app.services import llm_client
    audio = tmp_path / 'capture.webm'
    audio.write_bytes(b'audio')
    audio.with_suffix('.txt').write_text('周五提交报告。')
    capture.update_state(audio, status='summarizing')
    monkeypatch.setattr(capture, '_worker_id', 'restarted')
    assert capture.read_state(audio)['status'] == 'interrupted'
    monkeypatch.setattr(capture, 'capabilities', lambda: {'local_transcription_ready': False})
    monkeypatch.setattr(llm_client, 'siliconflow_chat', lambda *a, **kw: '待办：周五提交报告。')
    capture.process_recording(audio)
    assert capture.read_state(audio)['status'] == 'done'
