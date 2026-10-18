from datetime import datetime, timezone

import pytest
from fastapi import HTTPException
from sqlalchemy import create_engine
from sqlalchemy.orm import Session

from app.db import Base
from app.routers.configs import get_filters, set_data_filters
from app.services import wechat_gateway as gateway
from app.services.sync_service import _load_wechat_id_filters, _passes_wechat_filters


@pytest.fixture
def db(tmp_path):
    engine = create_engine(f'sqlite:///{tmp_path / "policies.db"}')
    Base.metadata.create_all(engine)
    with Session(engine) as session:
        yield session
    engine.dispose()


def data_lists(**overrides):
    return {**dict.fromkeys(('whitelist_senders', 'whitelist_talkers',
                             'blacklist_senders', 'blacklist_talkers'), []), **overrides}


def test_reply_and_data_lists_are_independent_in_storage_and_evaluation(db):
    gateway.save_config(db, {'enabled': True, 'outbound_enabled': True})
    gateway.save_trigger_rules(db, {'enabled': True, 'smart_reply_enabled': True,
        'private_enabled': True, 'prefixes': [], 'blacklist_chat_ids_enabled': True,
        'blacklist_chat_ids': ['reply-blocked']})
    before = gateway.load_trigger_rules(db)
    connection = gateway.load_config(db)
    saved = set_data_filters(data_lists(blacklist_talkers=[' data-blocked ', 'data-blocked']), db)
    assert saved['blacklist_talkers'] == ['data-blocked']
    assert gateway.load_trigger_rules(db) == before
    assert gateway.load_config(db) == connection
    filters = _load_wechat_id_filters(db)
    assert _passes_wechat_filters('reply-blocked', 'sender', filters)
    assert not _passes_wechat_filters('data-blocked', 'sender', filters)
    assert gateway.evaluate_auto_reply_rules(db, chat_id='data-blocked', sender_id='sender',
        text='hello', is_group=False)['allowed']
    assert gateway.evaluate_auto_reply_rules(db, chat_id='reply-blocked', sender_id='sender',
        text='hello', is_group=False)['reason'] == 'chat_blacklisted'
    assert gateway.evaluate_inbound_message(connection, chat_id='reply-blocked',
        sender_id='sender', text='hello')['action'] != 'drop'
    gateway.save_trigger_rules(db, {'blacklist_chat_ids': ['new-reply-blocked']})
    assert get_filters(db) == saved
    assert gateway.load_trigger_rules(db)['smart_reply_enabled'] is True


def test_data_filter_validation_cannot_partially_save_or_change_robot(db):
    saved = set_data_filters(data_lists(whitelist_talkers=['existing']), db)
    for payload in ({'whitelist_talkers': ['partial']}, data_lists(blacklist_senders=[3]),
                    {**data_lists(), 'smart_reply_enabled': True}):
        with pytest.raises(HTTPException):
            set_data_filters(payload, db)
        assert get_filters(db) == saved


@pytest.mark.parametrize('days,enabled,start,end,clock,reason', [
    ([0], True, '09:00', '18:00', '2026-10-19T02:00:00', 'passed'),
    ([1], True, '09:00', '18:00', '2026-10-19T02:00:00', 'inactive_day'),
    ([0], True, '09:00', '18:00', '2026-10-19T12:00:00', 'outside_schedule'),
    ([0], False, '09:00', '18:00', '2026-10-19T12:00:00', 'passed'),
    ([], False, '09:00', '18:00', '2026-10-19T12:00:00', 'inactive_day'),
    ([0], True, '22:00', '06:00', '2026-10-18T17:00:00', 'passed'),
    ([0], True, '09:00', '09:00', '2026-10-19T12:00:00', 'passed'),
])
def test_weekdays_and_hours_apply_together_in_beijing_time(db, days, enabled, start, end, clock, reason):
    result = gateway.evaluate_auto_reply_rules(db, chat_id='chat', sender_id='sender', text='hello',
        is_group=False, now=datetime.fromisoformat(clock).replace(tzinfo=timezone.utc),
        rules_override={'enabled': True, 'smart_reply_enabled': True, 'private_enabled': True,
            'prefixes': [], 'active_days': days, 'schedule_enabled': enabled,
            'schedule_start': start, 'schedule_end': end})
    assert result['reason'] == reason
