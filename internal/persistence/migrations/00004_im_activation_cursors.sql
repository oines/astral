-- Previously delivered IM events already had their activation scheduled by the
-- format-3 inbox. Begin the separate activation scan at that delivered boundary;
-- replaying it from a binding's start could reinterpret old Steer input IDs as
-- Context inputs. Leave the inbox, its revisions and all message history intact.
-- +goose Up
INSERT INTO documents(kind,id,revision,data)
SELECT 'im_activation_cursors',id,1,
       json_object('id',id,'revision',1,
                   'created_at',json_extract(data,'$.created_at'),
                   'updated_at',json_extract(data,'$.updated_at'),
                   'sequence',json_extract(data,'$.sequence'))
FROM documents
WHERE kind='inbox_cursors';

INSERT INTO versions(kind,id,revision,data)
SELECT kind,id,revision,data
FROM documents
WHERE kind='im_activation_cursors';
