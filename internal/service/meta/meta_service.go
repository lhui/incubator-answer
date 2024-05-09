/*
 * Licensed to the Apache Software Foundation (ASF) under one
 * or more contributor license agreements.  See the NOTICE file
 * distributed with this work for additional information
 * regarding copyright ownership.  The ASF licenses this file
 * to you under the Apache License, Version 2.0 (the
 * "License"); you may not use this file except in compliance
 * with the License.  You may obtain a copy of the License at
 *
 *   http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing,
 * software distributed under the License is distributed on an
 * "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY
 * KIND, either express or implied.  See the License for the
 * specific language governing permissions and limitations
 * under the License.
 */

package meta

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"github.com/apache/incubator-answer/internal/base/constant"
	"github.com/apache/incubator-answer/internal/base/handler"
	"github.com/apache/incubator-answer/internal/base/reason"
	"github.com/apache/incubator-answer/internal/base/translator"
	"github.com/apache/incubator-answer/internal/entity"
	"github.com/apache/incubator-answer/internal/schema"
	answercommon "github.com/apache/incubator-answer/internal/service/answer_common"
	metacommon "github.com/apache/incubator-answer/internal/service/meta_common"
	questioncommon "github.com/apache/incubator-answer/internal/service/question_common"
	usercommon "github.com/apache/incubator-answer/internal/service/user_common"
	"github.com/apache/incubator-answer/pkg/obj"
	myErrors "github.com/segmentfault/pacman/errors"
)

// MetaService user service
type MetaService struct {
	metaCommonService *metacommon.MetaCommonService
	userCommon        *usercommon.UserCommon
	questionRepo      questioncommon.QuestionRepo
	answerRepo        answercommon.AnswerRepo
}

func NewMetaService(metaCommonService *metacommon.MetaCommonService, userCommon *usercommon.UserCommon, answerRepo answercommon.AnswerRepo, questionRepo questioncommon.QuestionRepo) *MetaService {
	return &MetaService{
		metaCommonService: metaCommonService,
		questionRepo:      questionRepo,
		userCommon:        userCommon,
		answerRepo:        answerRepo,
	}
}

// GetReactionByObjectId get reaction
func (ms *MetaService) GetReactionByObjectId(ctx context.Context, req *schema.GetReactionReq) (resp *schema.ReactionResp, err error) {
	reactionMeta, err := ms.metaCommonService.GetMetaByObjectIdAndKey(ctx, req.ObjectID, entity.ObjectReactSummaryKey)

	// if not exist, return nil
	if err != nil {
		var pacmanErr *myErrors.Error
		if errors.As(err, &pacmanErr) && pacmanErr.Reason == reason.MetaObjectNotFound {
			return nil, nil
		} else {
			return nil, err
		}
	}

	var reaction schema.ReactionSummaryMeta
	err = json.Unmarshal([]byte(reactionMeta.Value), &reaction)
	if err != nil {
		return nil, err
	}
	return ms.convertToReactionResp(ctx, req.UserID, reaction)
}

// AddOrUpdateReaction add or update reaction
func (ms *MetaService) AddOrUpdateReaction(ctx context.Context, req *schema.UpdateReactionReq) (resp *schema.ReactionResp, err error) {
	// check if object exist and it's answer or question
	objectType, err := obj.GetObjectTypeStrByObjectID(req.ObjectID)
	if err != nil {
		return nil, err
	}
	if objectType == constant.AnswerObjectType {
		_, exist, err := ms.answerRepo.GetAnswer(ctx, req.ObjectID)
		if err != nil {
			return nil, err
		}
		if !exist {
			return nil, myErrors.BadRequest(reason.AnswerNotFound)
		}
	} else if objectType == constant.QuestionObjectType {
		_, exist, err := ms.questionRepo.GetQuestion(ctx, req.ObjectID)
		if err != nil {
			return nil, err
		}
		if !exist {
			return nil, myErrors.BadRequest(reason.QuestionNotFound)
		}
	} else {
		return nil, myErrors.BadRequest(reason.ObjectNotFound)
	}

	// add or update
	var reaction schema.ReactionSummaryMeta
	err = ms.metaCommonService.AddOrUpdateMetaByObjectIdAndKey(ctx, req.ObjectID, entity.ObjectReactSummaryKey, func(meta *entity.Meta, exist bool) (*entity.Meta, error) {
		// if not exist, create new one
		if !exist {
			reaction = schema.ReactionSummaryMeta{}
		} else {
			err = json.Unmarshal([]byte(meta.Value), &reaction)
			if err != nil {
				return nil, err
			}
		}

		// update reaction
		ms.updateReaction(req, reaction)

		// write back to meta repo
		reactSumBytes, err := json.Marshal(reaction)
		if err != nil {
			return nil, err
		}

		return &entity.Meta{
			ObjectID: req.ObjectID,
			Key:      entity.ObjectReactSummaryKey,
			Value:    string(reactSumBytes),
		}, nil
	})

	if err != nil {
		return nil, myErrors.InternalServer(reason.DatabaseError).WithError(err)
	}

	resp, err = ms.convertToReactionResp(ctx, req.UserID, reaction)
	if err != nil {
		return nil, err
	}

	return resp, nil
}

// updateReaction update reaction
func (ms *MetaService) updateReaction(req *schema.UpdateReactionReq, reaction schema.ReactionSummaryMeta) {
	emojiUserIds, ok := reaction[req.Emoji]

	if !ok {
		emojiUserIds = make([]string, 0)
	}

	found := false
	for _, item := range emojiUserIds {
		if item == req.UserID {
			found = true
			break
		}
	}

	removeItem := func(arr []string, target string) []string {
		result := make([]string, 0, len(arr))

		for _, item := range arr {
			if item != target {
				result = append(result, item)
			}
		}

		return result
	}

	if req.Reaction == "activate" && !found {
		emojiUserIds = append(emojiUserIds, req.UserID)
	} else if req.Reaction == "deactivate" && found {
		emojiUserIds = removeItem(emojiUserIds, req.UserID)
	}

	reaction[req.Emoji] = emojiUserIds
}

func (ms *MetaService) convertToReactionResp(ctx context.Context, userId string, reaction schema.ReactionSummaryMeta) (*schema.ReactionResp, error) {
	lang := handler.GetLangByCtx(ctx)
	resp := &schema.ReactionResp{
		ReactionSummary: make(map[string]*schema.ReactionItem),
	}
	isInArray := func(arr []string, target string) bool {
		for _, str := range arr {
			if str == target {
				return true
			}
		}
		return false
	}
	// traverse map and convert to username
	for emoji, userIds := range reaction {
		resp.ReactionSummary[emoji] = &schema.ReactionItem{
			Count:    len(userIds),
			IsActive: isInArray(userIds, userId),
		}
		userNames := make([]string, 0)
		userBasicInfos, err := ms.userCommon.BatchUserBasicInfoByID(ctx, userIds)
		if err != nil {
			return resp, err
		}
		// get username
		for _, userBasicInfo := range userBasicInfos {
			if len(userNames) == 5 {
				resp.ReactionSummary[emoji].Tooltip = translator.TrWithData(lang, constant.ReactionTooltipLabel, map[string]string{
					"Count": strconv.Itoa(len(userIds) - 5),
					"Names": strings.Join(userNames, ", "),
				})
				break
			}
			userNames = append(userNames, userBasicInfo.Username)
		}
		if len(userIds) <= 5 {
			resp.ReactionSummary[emoji].Tooltip = strings.Join(userNames, ", ")
		}
	}

	return resp, nil
}
